package service

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/goto/salt/log"
	"golang.org/x/net/context"

	"github.com/goto/optimus/config"
	"github.com/goto/optimus/core/scheduler"
	"github.com/goto/optimus/core/tenant"
	"github.com/goto/optimus/internal/errors"
	"github.com/goto/optimus/internal/lib/cron"
	"github.com/goto/optimus/internal/telemetry"
)

const (
	prefixReplayed = "replayed"
)

type ReplayScheduler interface {
	Clear(ctx context.Context, t tenant.Tenant, jobName scheduler.JobName, scheduledAt time.Time) error
	ClearBatch(ctx context.Context, t tenant.Tenant, jobName scheduler.JobName, startTime, endTime time.Time) error

	CreateRun(ctx context.Context, tnnt tenant.Tenant, jobName scheduler.JobName, executionTime time.Time, dagRunIDPrefix string) error
	GetJobRuns(ctx context.Context, t tenant.Tenant, criteria *scheduler.JobRunsCriteria, jobCron *cron.ScheduleSpec) ([]*scheduler.JobRunStatus, error)
}

type ReplayWorker struct {
	l log.Logger

	replayRepo ReplayRepository
	scheduler  ReplayScheduler

	jobRepo JobRepository

	config config.ReplayConfig
}

func NewReplayWorker(l log.Logger, replayRepo ReplayRepository, scheduler ReplayScheduler, jobRepo JobRepository, config config.ReplayConfig) *ReplayWorker {
	return &ReplayWorker{l: l, replayRepo: replayRepo, scheduler: scheduler, jobRepo: jobRepo, config: config}
}

type JobReplayRunService interface {
	GetJobRuns(ctx context.Context, projectName tenant.ProjectName, jobName scheduler.JobName, criteria *scheduler.JobRunsCriteria) ([]*scheduler.JobRunStatus, error)
}

func (w ReplayWorker) Process(replayReq *scheduler.ReplayWithRun) {
	ctx := context.Background()

	w.l.Debug("processing replay request %s with status %s", replayReq.Replay.ID().String(), replayReq.Replay.State().String())
	jobCron, err := getJobCron(ctx, w.l, w.jobRepo, replayReq.Replay.Tenant(), replayReq.Replay.JobName())
	if err != nil {
		w.l.Error("unable to get cron value for job [%s] replay id [%s]: %s", replayReq.Replay.JobName().String(), replayReq.Replay.ID().String(), err)
		w.updateReplayAsFailed(ctx, replayReq.Replay.ID(), err.Error())
		raiseReplayMetric(replayReq.Replay.Tenant(), replayReq.Replay.JobName(), scheduler.ReplayStateFailed)
		return
	}

	switch replayReq.Replay.State() {
	case scheduler.ReplayStateCreated:
		err = w.processNewReplayRequest(ctx, replayReq, jobCron)
	case scheduler.ReplayStatePartialReplayed:
		err = w.processPartialReplayedRequest(ctx, replayReq, jobCron)
	case scheduler.ReplayStateReplayed:
		err = w.processReplayedRequest(ctx, replayReq, jobCron)
	}

	if err != nil {
		w.l.Error("error encountered when processing replay request: %s", err)
		w.updateReplayAsFailed(ctx, replayReq.Replay.ID(), err.Error())
		raiseReplayMetric(replayReq.Replay.Tenant(), replayReq.Replay.JobName(), scheduler.ReplayStateFailed)
	}
}

func (w ReplayWorker) createMissingRuns(ctx context.Context, replayReq *scheduler.ReplayWithRun, jobCron *cron.ScheduleSpec) error {
	// fetch runs within range of replay range
	existedRuns, err := w.fetchRuns(ctx, replayReq, jobCron)
	if err != nil {
		return err
	}

	// check each runs if there's no existing run from the above
	runsToBeCreated := getMissingRuns(replayReq.Runs, existedRuns)
	w.l.Info("create %d missing runs with replay id %s", len(runsToBeCreated), replayReq.Replay.ID().String())
	me := errors.NewMultiError("create runs")
	for _, run := range runsToBeCreated {
		// create missing runs
		if err := w.scheduler.CreateRun(ctx, replayReq.Replay.Tenant(), replayReq.Replay.JobName(), run.GetLogicalTime(jobCron), prefixReplayed); err != nil {
			me.Append(err)
		}
	}

	return me.ToErr()
}

func getMissingRuns(expectedRuns, existingRuns []*scheduler.JobRunStatus) []*scheduler.JobRunStatus {
	runsToBeCreated := []*scheduler.JobRunStatus{}
	existedRunsMap := scheduler.JobRunStatusList(existingRuns).ToRunStatusMap()
	for _, run := range expectedRuns {
		if _, ok := existedRunsMap[run.ScheduledAt]; !ok {
			runsToBeCreated = append(runsToBeCreated, run)
		}
	}
	return runsToBeCreated
}

func (w ReplayWorker) processNewReplayRequest(ctx context.Context, replayReq *scheduler.ReplayWithRun, jobCron *cron.ScheduleSpec) (err error) {
	state := scheduler.ReplayStateReplayed
	if !replayReq.Replay.Config().Parallel && len(replayReq.Runs) > 1 {
		state = scheduler.ReplayStatePartialReplayed
	}
	var updatedRuns []*scheduler.JobRunStatus
	if replayReq.Replay.Config().Parallel {
		updatedRuns, err = w.processNewReplayRequestParallel(ctx, replayReq, jobCron)
	} else {
		updatedRuns, err = w.processNewReplayRequestSequential(ctx, replayReq, jobCron)
	}
	if err != nil {
		w.l.Error("error processing new replay: %s", err)
		return err
	}

	if err := w.replayRepo.UpdateReplay(ctx, replayReq.Replay.ID(), state, updatedRuns, ""); err != nil {
		w.l.Error("unable to update replay state for replay_id [%s]: %s", replayReq.Replay.ID().String(), err)
		return err
	}
	raiseReplayMetric(replayReq.Replay.Tenant(), replayReq.Replay.JobName(), state)
	return nil
}

func (w ReplayWorker) processNewReplayRequestParallel(ctx context.Context, replayReq *scheduler.ReplayWithRun, jobCron *cron.ScheduleSpec) ([]*scheduler.JobRunStatus, error) {
	startLogicalTime := replayReq.GetFirstExecutableRun().GetLogicalTime(jobCron)
	endLogicalTime := replayReq.GetLastExecutableRun().GetLogicalTime(jobCron)
	if err := w.scheduler.ClearBatch(ctx, replayReq.Replay.Tenant(), replayReq.Replay.JobName(), startLogicalTime, endLogicalTime); err != nil {
		w.l.Error("unable to clear job run for replay with replay_id [%s]: %s", replayReq.Replay.ID().String(), err)
		return nil, err
	}
	if err := w.createMissingRuns(ctx, replayReq, jobCron); err != nil {
		w.l.Error("unable to create missing runs for replay with replay_id [%s]: %s", replayReq.Replay.ID().String(), err)
		return nil, err
	}

	w.l.Info("cleared/created [%s] runs for replay [%s]", replayReq.Replay.JobName().String(), replayReq.Replay.ID().String())

	var updatedRuns []*scheduler.JobRunStatus
	for _, run := range replayReq.Runs {
		updatedRuns = append(updatedRuns, &scheduler.JobRunStatus{ScheduledAt: run.ScheduledAt, State: scheduler.StateInProgress})
	}
	return updatedRuns, nil
}

func (w ReplayWorker) processNewReplayRequestSequential(ctx context.Context, replayReq *scheduler.ReplayWithRun, jobCron *cron.ScheduleSpec) ([]*scheduler.JobRunStatus, error) {
	runToReplay := replayReq.GetFirstExecutableRun()
	if runToReplay == nil {
		return replayReq.Runs, nil
	}
	if err := w.replayRunOnScheduler(ctx, replayReq, jobCron, runToReplay); err != nil {
		return nil, err
	}
	updatedReplayMap := map[time.Time]scheduler.State{
		runToReplay.ScheduledAt: scheduler.StateInProgress,
	}
	updatedRuns := scheduler.JobRunStatusList(replayReq.Runs).MergeWithUpdatedRuns(updatedReplayMap)
	return updatedRuns, nil
}

func (w ReplayWorker) processPartialReplayedRequest(ctx context.Context, replayReq *scheduler.ReplayWithRun, jobCron *cron.ScheduleSpec) error {
	incomingRuns, err := w.fetchRuns(ctx, replayReq, jobCron)
	if err != nil {
		w.l.Error("unable to get runs for replay [%s]: %s", replayReq.Replay.ID().String(), err)
		return err
	}

	updatedReplayMap := identifyUpdatedRunStatus(replayReq.Runs, incomingRuns)
	updatedRuns := scheduler.JobRunStatusList(replayReq.Runs).MergeWithUpdatedRuns(updatedReplayMap)

	replayedRuns := scheduler.JobRunStatusList(updatedRuns).GetSortedRunsByStates([]scheduler.State{scheduler.StateInProgress})
	toBeReplayedRuns := scheduler.JobRunStatusList(updatedRuns).GetSortedRunsByStates([]scheduler.State{scheduler.StatePending})

	replayState := scheduler.ReplayStatePartialReplayed
	if len(replayedRuns) == 0 && len(toBeReplayedRuns) > 0 {
		runToReplay := toBeReplayedRuns[0]
		if err := w.replayRunOnScheduler(ctx, replayReq, jobCron, runToReplay); err != nil {
			return err
		}

		updatedReplayMap[runToReplay.ScheduledAt] = scheduler.StateInProgress
		updatedRuns = scheduler.JobRunStatusList(updatedRuns).MergeWithUpdatedRuns(updatedReplayMap)
	}

	pendingRuns := scheduler.JobRunStatusList(updatedRuns).GetSortedRunsByStates([]scheduler.State{scheduler.StatePending})
	if len(pendingRuns) == 0 {
		replayState = scheduler.ReplayStateReplayed
	}

	if err := w.replayRepo.UpdateReplay(ctx, replayReq.Replay.ID(), replayState, updatedRuns, ""); err != nil {
		w.l.Error("unable to update replay state for replay_id [%s]: %s", replayReq.Replay.ID().String(), err)
		return err
	}
	raiseReplayMetric(replayReq.Replay.Tenant(), replayReq.Replay.JobName(), replayState)
	return nil
}

// replayRunOnScheduler create run if targeted run is not exist, otherwise clear existing run on scheduler
func (w ReplayWorker) replayRunOnScheduler(ctx context.Context, replayReq *scheduler.ReplayWithRun, jobCron *cron.ScheduleSpec, runToReplay *scheduler.JobRunStatus) error {
	_, err := w.fetchRun(ctx, replayReq, jobCron, runToReplay.ScheduledAt)
	if err != nil && errors.IsErrorType(err, errors.ErrNotFound) {
		if err := w.scheduler.CreateRun(ctx, replayReq.Replay.Tenant(), replayReq.Replay.JobName(), runToReplay.GetLogicalTime(jobCron), prefixReplayed); err != nil {
			w.l.Error("unable to create missing runs for replay with replay_id [%s] with logical time %s: %s", replayReq.Replay.ID().String(), runToReplay.GetLogicalTime(jobCron), err)
			return err
		}
		w.l.Info("created [%s] [%s] run for replay %s", replayReq.Replay.JobName().String(), runToReplay.ScheduledAt, replayReq.Replay.ID().String())
	} else if err != nil {
		return err
	} else {
		if err := w.scheduler.Clear(ctx, replayReq.Replay.Tenant(), replayReq.Replay.JobName(), runToReplay.GetLogicalTime(jobCron)); err != nil {
			w.l.Error("unable to clear job run for replay with replay_id [%s]: %s", replayReq.Replay.ID().String(), err)
			return err
		}
		w.l.Info("cleared [%s] [%s] run for replay %s", replayReq.Replay.JobName().String(), runToReplay.ScheduledAt, replayReq.Replay.ID().String())
	}
	return nil
}

func (w ReplayWorker) processReplayedRequest(ctx context.Context, replayReq *scheduler.ReplayWithRun, jobCron *cron.ScheduleSpec) error {
	incomingRuns, err := w.fetchRuns(ctx, replayReq, jobCron)
	if err != nil {
		w.l.Error("unable to get runs for replay with replay_id [%s]: %s", replayReq.Replay.ID().String(), err)
		return err
	}

	updatedReplayMap := identifyUpdatedRunStatus(replayReq.Runs, incomingRuns)
	updatedRuns := scheduler.JobRunStatusList(replayReq.Runs).MergeWithUpdatedRuns(updatedReplayMap)
	inProgressRuns := scheduler.JobRunStatusList(updatedRuns).GetSortedRunsByStates([]scheduler.State{scheduler.StateInProgress})
	failedRuns := scheduler.JobRunStatusList(updatedRuns).GetSortedRunsByStates([]scheduler.State{scheduler.StateFailed})

	var message string
	state := scheduler.ReplayStateReplayed
	if len(inProgressRuns) == 0 && len(failedRuns) == 0 {
		state = scheduler.ReplayStateSuccess
		w.l.Info("marking replay %s as success", replayReq.Replay.ID().String())
	} else if len(inProgressRuns) == 0 && len(failedRuns) > 0 {
		state = scheduler.ReplayStateFailed
		message = fmt.Sprintf("found %d failed runs.", len(failedRuns))
		w.l.Info("marking replay %s as failed", replayReq.Replay.ID().String())
	}

	if err := w.replayRepo.UpdateReplay(ctx, replayReq.Replay.ID(), state, updatedRuns, message); err != nil {
		w.l.Error("unable to update replay with replay_id [%s]: %s", replayReq.Replay.ID().String(), err)
		return err
	}
	raiseReplayMetric(replayReq.Replay.Tenant(), replayReq.Replay.JobName(), state)
	return nil
}

func identifyUpdatedRunStatus(existingJobRuns, incomingJobRuns []*scheduler.JobRunStatus) map[time.Time]scheduler.State {
	incomingRunStatusMap := scheduler.JobRunStatusList(incomingJobRuns).ToRunStatusMap()

	updatedReplayMap := make(map[time.Time]scheduler.State)
	for _, run := range existingJobRuns {
		if run.State != scheduler.StateInProgress {
			continue
		}
		if incomingRunStatusMap[run.ScheduledAt.UTC()] == scheduler.StateSuccess || incomingRunStatusMap[run.ScheduledAt.UTC()] == scheduler.StateFailed {
			updatedReplayMap[run.ScheduledAt.UTC()] = incomingRunStatusMap[run.ScheduledAt.UTC()]
		}
	}
	return updatedReplayMap
}

func (w ReplayWorker) fetchRun(ctx context.Context, replayReq *scheduler.ReplayWithRun, jobCron *cron.ScheduleSpec, scheduledAt time.Time) (*scheduler.JobRunStatus, error) {
	jobRunCriteria := &scheduler.JobRunsCriteria{
		Name:      replayReq.Replay.JobName().String(),
		StartDate: scheduledAt,
		EndDate:   scheduledAt,
	}
	runs, err := w.scheduler.GetJobRuns(ctx, replayReq.Replay.Tenant(), jobRunCriteria, jobCron)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, errors.NotFound(scheduler.EntityReplay, fmt.Sprintf("run on job %s with scheduled_at %s not exist", replayReq.Replay.JobName().String(), scheduledAt))
	}
	return runs[0], nil
}

func (w ReplayWorker) fetchRuns(ctx context.Context, replayReq *scheduler.ReplayWithRun, jobCron *cron.ScheduleSpec) ([]*scheduler.JobRunStatus, error) {
	jobRunCriteria := &scheduler.JobRunsCriteria{
		Name:      replayReq.Replay.JobName().String(),
		StartDate: replayReq.Replay.Config().StartTime,
		EndDate:   replayReq.Replay.Config().EndTime,
	}
	return w.scheduler.GetJobRuns(ctx, replayReq.Replay.Tenant(), jobRunCriteria, jobCron)
}

func (w ReplayWorker) updateReplayAsFailed(ctx context.Context, replayID uuid.UUID, message string) {
	if err := w.replayRepo.UpdateReplayStatus(ctx, replayID, scheduler.ReplayStateFailed, message); err != nil {
		w.l.Error("unable to update replay state to failed for replay_id [%s]: %s", replayID, err)
	}
}

func raiseReplayMetric(t tenant.Tenant, jobName scheduler.JobName, state scheduler.ReplayState) {
	telemetry.NewCounter(metricJobReplay, map[string]string{
		"project":   t.ProjectName().String(),
		"namespace": t.NamespaceName().String(),
		"job":       jobName.String(),
		"status":    state.String(),
	}).Inc()
}
