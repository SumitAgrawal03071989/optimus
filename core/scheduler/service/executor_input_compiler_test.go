package service_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/goto/salt/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/goto/optimus/core/scheduler"
	"github.com/goto/optimus/core/scheduler/service"
	"github.com/goto/optimus/core/tenant"
	"github.com/goto/optimus/internal/lib/window"
	"github.com/goto/optimus/internal/models"
)

func TestExecutorCompiler(t *testing.T) {
	ctx := context.Background()

	project, _ := tenant.NewProject("proj1", map[string]string{
		"STORAGE_PATH":   "somePath",
		"SCHEDULER_HOST": "localhost",
	})
	namespace, _ := tenant.NewNamespace("ns1", project.Name(), map[string]string{})

	secret1, _ := tenant.NewPlainTextSecret("secretName", "secretValue")
	secret2, _ := tenant.NewPlainTextSecret("secret2Name", "secret2Value")
	secretsArray := []*tenant.PlainTextSecret{secret1, secret2}

	tenantDetails, _ := tenant.NewTenantDetails(project, namespace, secretsArray)
	tnnt, _ := tenant.NewTenant(project.Name().String(), namespace.Name().String())

	currentTime := time.Now()
	scheduleTime := currentTime.Add(-time.Hour)

	logger := log.NewLogrus()

	t.Run("Compile", func(t *testing.T) {
		t.Run("should give error if tenant service getDetails fails", func(t *testing.T) {
			job := scheduler.Job{
				Name:   "job1",
				Tenant: tnnt,
			}
			details := scheduler.JobWithDetails{Job: &job}

			config := scheduler.RunConfig{
				Executor: scheduler.Executor{
					Name: "transformer",
					Type: "bq2bq",
				},
				ScheduledAt: scheduleTime,
				JobRunID:    scheduler.JobRunID{},
			}

			tenantService := new(mockTenantService)
			tenantService.On("GetDetails", ctx, tnnt).Return(nil, fmt.Errorf("get details error"))
			defer tenantService.AssertExpectations(t)

			inputCompiler := service.NewJobInputCompiler(tenantService, nil, nil, logger)
			inputExecutor, err := inputCompiler.Compile(ctx, &details, config, currentTime.Add(time.Hour))

			assert.NotNil(t, err)
			assert.EqualError(t, err, "get details error")
			assert.Nil(t, inputExecutor)
		})
		t.Run("should give error if get interval fails", func(t *testing.T) {
			w1, _ := models.NewWindow(1, "d", "2h", "2")
			window1 := window.NewCustomConfig(w1)
			job := scheduler.Job{
				Name:         "job1",
				Tenant:       tnnt,
				WindowConfig: window1,
			}
			details := scheduler.JobWithDetails{
				Job: &job,
				Schedule: &scheduler.Schedule{
					Interval: "0 * * * *",
				},
			}

			config := scheduler.RunConfig{
				Executor: scheduler.Executor{
					Name: "transformer",
					Type: "bq2bq",
				},
				ScheduledAt: currentTime.Add(-time.Hour),
				JobRunID:    scheduler.JobRunID{},
			}

			tenantService := new(mockTenantService)
			tenantService.On("GetDetails", ctx, tnnt).Return(tenantDetails, nil)
			defer tenantService.AssertExpectations(t)

			inputCompiler := service.NewJobInputCompiler(tenantService, nil, nil, logger)
			inputExecutor, err := inputCompiler.Compile(ctx, &details, config, currentTime.Add(time.Hour))

			assert.NotNil(t, err)
			assert.EqualError(t, err, "failed to parse task window with size 2: time: missing unit in duration \"2\"")
			assert.Nil(t, inputExecutor)
		})
		t.Run("should give error if CompileJobRunAssets fails", func(t *testing.T) {
			w, _ := models.NewWindow(2, "d", "1h", "24h")
			cw := window.NewCustomConfig(w)
			job := scheduler.Job{
				Name:         "job1",
				Tenant:       tnnt,
				WindowConfig: cw,
				Assets:       nil,
			}
			details := scheduler.JobWithDetails{
				Job: &job,
				Schedule: &scheduler.Schedule{
					Interval: "0 * * * *",
				},
			}
			config := scheduler.RunConfig{
				Executor: scheduler.Executor{
					Name: "transformer",
					Type: "bq2bq",
				},
				ScheduledAt: currentTime.Add(-time.Hour),
				JobRunID:    scheduler.JobRunID{},
			}

			tenantService := new(mockTenantService)
			tenantService.On("GetDetails", ctx, tnnt).Return(tenantDetails, nil)
			defer tenantService.AssertExpectations(t)

			interval, err := window.FromBaseWindow(w).GetInterval(config.ScheduledAt)
			assert.NoError(t, err)
			executedAt := currentTime.Add(time.Hour)
			systemDefinedVars := map[string]string{
				"DSTART":          interval.Start.Format(time.RFC3339),
				"DEND":            interval.End.Format(time.RFC3339),
				"EXECUTION_TIME":  executedAt.Format(time.RFC3339),
				"JOB_DESTINATION": job.Destination,
			}
			taskContext := mock.Anything

			assetCompiler := new(mockAssetCompiler)
			assetCompiler.On("CompileJobRunAssets", ctx, &job, systemDefinedVars, interval, taskContext).Return(nil, fmt.Errorf("CompileJobRunAssets error"))
			defer assetCompiler.AssertExpectations(t)

			inputCompiler := service.NewJobInputCompiler(tenantService, nil, assetCompiler, logger)
			inputExecutor, err := inputCompiler.Compile(ctx, &details, config, executedAt)

			assert.NotNil(t, err)
			assert.EqualError(t, err, "CompileJobRunAssets error")
			assert.Nil(t, inputExecutor)
		})
		t.Run("compileConfigs for Executor type Task ", func(t *testing.T) {
			w1, _ := models.NewWindow(2, "d", "1h", "24h")
			window1 := window.NewCustomConfig(w1)
			job := scheduler.Job{
				Name:        "job1",
				Tenant:      tnnt,
				Destination: "some_destination_table_name",
				Task: &scheduler.Task{
					Name: "bq2bq",
					Config: map[string]string{
						"secret.config": "a.secret.val",
						"some.config":   "val",
					},
				},
				Hooks:        nil,
				WindowConfig: window1,
				Assets:       nil,
			}
			details := scheduler.JobWithDetails{
				Job: &job,
				Schedule: &scheduler.Schedule{
					Interval: "0 * * * *",
				},
			}
			config := scheduler.RunConfig{
				Executor: scheduler.Executor{
					Name: "bq2bq",
					Type: scheduler.ExecutorTask,
				},
				ScheduledAt: currentTime.Add(-time.Hour),
				JobRunID:    scheduler.JobRunID{},
			}

			tenantService := new(mockTenantService)
			tenantService.On("GetDetails", ctx, tnnt).Return(tenantDetails, nil)
			defer tenantService.AssertExpectations(t)

			interval, err := window.FromBaseWindow(w1).GetInterval(config.ScheduledAt)
			assert.NoError(t, err)

			executedAt := currentTime.Add(time.Hour)
			systemDefinedVars := map[string]string{
				"DSTART":          interval.Start.Format(time.RFC3339),
				"DEND":            interval.End.Format(time.RFC3339),
				"EXECUTION_TIME":  executedAt.Format(time.RFC3339),
				"JOB_DESTINATION": job.Destination,
			}
			taskContext := mock.Anything

			compiledFile := map[string]string{
				"someFileName": "fileContents",
			}

			t.Run("should give error if compileConfigs conf compilation fails", func(t *testing.T) {
				templateCompiler := new(mockTemplateCompiler)
				templateCompiler.On("Compile", map[string]string{"some.config": "val"}, taskContext).
					Return(nil, fmt.Errorf("some.config compilation error"))
				defer templateCompiler.AssertExpectations(t)
				assetCompiler := new(mockAssetCompiler)
				assetCompiler.On("CompileJobRunAssets", ctx, &job, systemDefinedVars, interval, taskContext).Return(compiledFile, nil)
				defer assetCompiler.AssertExpectations(t)
				inputCompiler := service.NewJobInputCompiler(tenantService, templateCompiler, assetCompiler, logger)
				inputExecutor, err := inputCompiler.Compile(ctx, &details, config, executedAt)

				assert.NotNil(t, err)
				assert.EqualError(t, err, "some.config compilation error")
				assert.Nil(t, inputExecutor)
			})
			t.Run("should give error if compileConfigs secret compilation fails", func(t *testing.T) {
				templateCompiler := new(mockTemplateCompiler)
				templateCompiler.On("Compile", map[string]string{"some.config": "val"}, taskContext).
					Return(map[string]string{"some.config.compiled": "val.compiled"}, nil)
				templateCompiler.On("Compile", map[string]string{"secret.config": "a.secret.val"}, taskContext).
					Return(nil, fmt.Errorf("secret.config compilation error"))
				defer templateCompiler.AssertExpectations(t)
				assetCompiler := new(mockAssetCompiler)
				assetCompiler.On("CompileJobRunAssets", ctx, &job, systemDefinedVars, interval, taskContext).Return(compiledFile, nil)
				defer assetCompiler.AssertExpectations(t)
				inputCompiler := service.NewJobInputCompiler(tenantService, templateCompiler, assetCompiler, logger)
				inputExecutor, err := inputCompiler.Compile(ctx, &details, config, executedAt)

				assert.NotNil(t, err)
				assert.EqualError(t, err, "secret.config compilation error")
				assert.Nil(t, inputExecutor)
			})
			t.Run("should return successfully and provide expected ExecutorInput", func(t *testing.T) {
				templateCompiler := new(mockTemplateCompiler)
				templateCompiler.On("Compile", map[string]string{"some.config": "val"}, taskContext).
					Return(map[string]string{"some.config.compiled": "val.compiled"}, nil)
				templateCompiler.On("Compile", map[string]string{"secret.config": "a.secret.val"}, taskContext).
					Return(map[string]string{"secret.config.compiled": "a.secret.val.compiled"}, nil)
				defer templateCompiler.AssertExpectations(t)
				assetCompiler := new(mockAssetCompiler)
				assetCompiler.On("CompileJobRunAssets", ctx, &job, systemDefinedVars, interval, taskContext).Return(compiledFile, nil)
				defer assetCompiler.AssertExpectations(t)
				inputCompiler := service.NewJobInputCompiler(tenantService, templateCompiler, assetCompiler, logger)
				inputExecutorResp, err := inputCompiler.Compile(ctx, &details, config, executedAt)

				assert.Nil(t, err)
				expectedInputExecutor := &scheduler.ExecutorInput{
					Configs: map[string]string{
						"DSTART":               interval.Start.Format(time.RFC3339),
						"DEND":                 interval.End.Format(time.RFC3339),
						"EXECUTION_TIME":       executedAt.Format(time.RFC3339),
						"JOB_DESTINATION":      job.Destination,
						"some.config.compiled": "val.compiled",
					},
					Secrets: map[string]string{"secret.config.compiled": "a.secret.val.compiled"},
					Files:   compiledFile,
				}
				expectedJobLabels := map[string]bool{
					"project=proj1": true,
					"namespace=ns1": true,
					"job_name=job1": true,
					"job_id=00000000-0000-0000-0000-000000000000": true,
				}

				for _, v := range strings.Split(inputExecutorResp.Configs["JOB_LABELS"], ",") {
					_, ok := expectedJobLabels[v]
					assert.True(t, ok)
				}
				delete(inputExecutorResp.Configs, "JOB_LABELS")
				assert.Equal(t, expectedInputExecutor, inputExecutorResp)
			})
			t.Run("should return successfully and sanitise job labels ", func(t *testing.T) {
				templateCompiler := new(mockTemplateCompiler)
				templateCompiler.On("Compile", map[string]string{"some.config": "val"}, taskContext).
					Return(map[string]string{"some.config.compiled": "val.compiled"}, nil)
				templateCompiler.On("Compile", map[string]string{"secret.config": "a.secret.val"}, taskContext).
					Return(map[string]string{"secret.config.compiled": "a.secret.val.compiled"}, nil)
				defer templateCompiler.AssertExpectations(t)

				jobNew := job
				jobNew.ID = uuid.New()
				jobNew.Name = "nameWith Invalid~Characters)(Which Are.even.LongerThan^63Charancters"
				detailsNew := scheduler.JobWithDetails{
					Job:      &jobNew,
					Schedule: &scheduler.Schedule{Interval: "0 0 1 * *"},
				}

				assetCompilerNew := new(mockAssetCompiler)
				assetCompilerNew.On("CompileJobRunAssets", ctx, &jobNew, systemDefinedVars, interval, taskContext).Return(compiledFile, nil)
				defer assetCompilerNew.AssertExpectations(t)

				inputCompiler := service.NewJobInputCompiler(tenantService, templateCompiler, assetCompilerNew, logger)

				inputExecutorResp, err := inputCompiler.Compile(ctx, &detailsNew, config, executedAt)
				assert.Nil(t, err)

				expectedInputExecutor := &scheduler.ExecutorInput{
					Configs: map[string]string{
						"DSTART":               interval.Start.Format(time.RFC3339),
						"DEND":                 interval.End.Format(time.RFC3339),
						"EXECUTION_TIME":       executedAt.Format(time.RFC3339),
						"JOB_DESTINATION":      job.Destination,
						"some.config.compiled": "val.compiled",
					},
					Secrets: map[string]string{"secret.config.compiled": "a.secret.val.compiled"},
					Files:   compiledFile,
				}

				jobIDLabel := fmt.Sprintf("job_id=%s", jobNew.ID)
				expecrtedJobLabels := map[string]bool{
					"project=proj1": true,
					"namespace=ns1": true,
					"job_name=__h-invalid-characters--which-are-even-longerthan-63charancters": true,
					jobIDLabel: true,
				}
				for _, v := range strings.Split(inputExecutorResp.Configs["JOB_LABELS"], ",") {
					_, ok := expecrtedJobLabels[v]
					assert.True(t, ok)
				}
				delete(inputExecutorResp.Configs, "JOB_LABELS")
				assert.Equal(t, expectedInputExecutor, inputExecutorResp)
			})
		})
		t.Run("compileConfigs for Executor type Hook", func(t *testing.T) {
			w1, _ := models.NewWindow(2, "d", "1h", "24h")
			window1 := window.NewCustomConfig(w1)
			job := scheduler.Job{
				Name:        "job1",
				Tenant:      tnnt,
				Destination: "some_destination_table_name",
				Task: &scheduler.Task{
					Name: "bq2bq",
					Config: map[string]string{
						"secret.config": "a.secret.val",
						"some.config":   "val",
					},
				},
				Hooks: []*scheduler.Hook{
					{
						Name: "predator",
						Config: map[string]string{
							"hook_secret":      "a.secret.val",
							"hook_some_config": "val",
						},
					},
				},
				WindowConfig: window1,
				Assets:       nil,
			}
			details := scheduler.JobWithDetails{
				Job: &job,
				Schedule: &scheduler.Schedule{
					Interval: "0 * * * *",
				},
			}
			config := scheduler.RunConfig{
				Executor: scheduler.Executor{
					Name: "predator",
					Type: scheduler.ExecutorHook,
				},
				ScheduledAt: currentTime.Add(-time.Hour),
				JobRunID:    scheduler.JobRunID{},
			}

			tenantService := new(mockTenantService)
			tenantService.On("GetDetails", ctx, tnnt).Return(tenantDetails, nil)
			defer tenantService.AssertExpectations(t)

			interval, err := window.FromBaseWindow(w1).GetInterval(config.ScheduledAt)
			assert.NoError(t, err)
			executedAt := currentTime.Add(time.Hour)
			systemDefinedVars := map[string]string{
				"DSTART":          interval.Start.Format(time.RFC3339),
				"DEND":            interval.End.Format(time.RFC3339),
				"EXECUTION_TIME":  executedAt.Format(time.RFC3339),
				"JOB_DESTINATION": job.Destination,
			}
			taskContext := mock.Anything

			compiledFile := map[string]string{
				"someFileName": "fileContents",
			}
			assetCompiler := new(mockAssetCompiler)
			assetCompiler.On("CompileJobRunAssets", ctx, &job, systemDefinedVars, interval, taskContext).Return(compiledFile, nil)
			defer assetCompiler.AssertExpectations(t)

			templateCompiler := new(mockTemplateCompiler)
			templateCompiler.On("Compile", map[string]string{"some.config": "val"}, taskContext).
				Return(map[string]string{"some.config.compiled": "val.compiled"}, nil)
			templateCompiler.On("Compile", map[string]string{"secret.config": "a.secret.val"}, taskContext).
				Return(map[string]string{"secret.config.compiled": "a.secret.val.compiled"}, nil)
			templateCompiler.On("Compile", map[string]string{"hook_some_config": "val"}, taskContext).
				Return(map[string]string{"hook.compiled": "hook.val.compiled"}, nil)
			templateCompiler.On("Compile", map[string]string{"hook_secret": "a.secret.val"}, taskContext).
				Return(map[string]string{"secret.hook.compiled": "hook.s.val.compiled"}, nil)
			defer templateCompiler.AssertExpectations(t)

			inputCompiler := service.NewJobInputCompiler(tenantService, templateCompiler, assetCompiler, logger)
			inputExecutorResp, err := inputCompiler.Compile(ctx, &details, config, executedAt)

			assert.Nil(t, err)
			expectedInputExecutor := &scheduler.ExecutorInput{
				Configs: map[string]string{
					"DSTART":          interval.Start.Format(time.RFC3339),
					"DEND":            interval.End.Format(time.RFC3339),
					"EXECUTION_TIME":  executedAt.Format(time.RFC3339),
					"JOB_DESTINATION": job.Destination,
					"hook.compiled":   "hook.val.compiled",
				},
				Secrets: map[string]string{"secret.hook.compiled": "hook.s.val.compiled"},
				Files:   compiledFile,
			}
			assert.Equal(t, expectedInputExecutor, inputExecutorResp)
		})
		t.Run("compileConfigs for Executor type Hook should fail if error in hook compilation", func(t *testing.T) {
			w1, _ := models.NewWindow(2, "d", "1h", "24h")
			window1 := window.NewCustomConfig(w1)
			job := scheduler.Job{
				Name:        "job1",
				Tenant:      tnnt,
				Destination: "some_destination_table_name",
				Task: &scheduler.Task{
					Name: "bq2bq",
					Config: map[string]string{
						"secret.config": "a.secret.val",
						"some.config":   "val",
					},
				},
				Hooks: []*scheduler.Hook{
					{
						Name: "predator",
						Config: map[string]string{
							"hook_secret":      "a.secret.val",
							"hook_some_config": "val",
						},
					},
				},
				WindowConfig: window1,
				Assets:       nil,
			}
			details := scheduler.JobWithDetails{
				Job: &job,
				Schedule: &scheduler.Schedule{
					Interval: "0 * * * *",
				},
			}
			config := scheduler.RunConfig{
				Executor: scheduler.Executor{
					Name: "predator",
					Type: scheduler.ExecutorHook,
				},
				ScheduledAt: currentTime.Add(-time.Hour),
				JobRunID:    scheduler.JobRunID{},
			}

			tenantService := new(mockTenantService)
			tenantService.On("GetDetails", ctx, tnnt).Return(tenantDetails, nil)
			defer tenantService.AssertExpectations(t)

			interval, err := window.FromBaseWindow(w1).GetInterval(config.ScheduledAt)
			assert.NoError(t, err)
			executedAt := currentTime.Add(time.Hour)
			systemDefinedVars := map[string]string{
				"DSTART":          interval.Start.Format(time.RFC3339),
				"DEND":            interval.End.Format(time.RFC3339),
				"EXECUTION_TIME":  executedAt.Format(time.RFC3339),
				"JOB_DESTINATION": job.Destination,
			}
			taskContext := mock.Anything

			compiledFile := map[string]string{
				"someFileName": "fileContents",
			}
			assetCompiler := new(mockAssetCompiler)
			assetCompiler.On("CompileJobRunAssets", ctx, &job, systemDefinedVars, interval, taskContext).Return(compiledFile, nil)
			defer assetCompiler.AssertExpectations(t)

			templateCompiler := new(mockTemplateCompiler)
			templateCompiler.On("Compile", map[string]string{"some.config": "val"}, taskContext).
				Return(map[string]string{"some.config.compiled": "val.compiled"}, nil)
			templateCompiler.On("Compile", map[string]string{"secret.config": "a.secret.val"}, taskContext).
				Return(map[string]string{"secret.config.compiled": "a.secret.val.compiled"}, nil)
			templateCompiler.On("Compile", map[string]string{"hook_some_config": "val"}, taskContext).
				Return(nil, fmt.Errorf("error in compiling hook template"))

			defer templateCompiler.AssertExpectations(t)

			inputCompiler := service.NewJobInputCompiler(tenantService, templateCompiler, assetCompiler, logger)
			inputExecutorResp, err := inputCompiler.Compile(ctx, &details, config, executedAt)

			assert.NotNil(t, err)
			assert.EqualError(t, err, "error in compiling hook template")
			assert.Nil(t, inputExecutorResp)
		})
		t.Run("compileConfigs for Executor type Hook, should raise error if hooks not there in job", func(t *testing.T) {
			w1, _ := models.NewWindow(2, "d", "1h", "24h")
			window1 := window.NewCustomConfig(w1)
			job := scheduler.Job{
				Name:        "job1",
				Tenant:      tnnt,
				Destination: "some_destination_table_name",
				Task: &scheduler.Task{
					Name: "bq2bq",
					Config: map[string]string{
						"secret.config": "a.secret.val",
						"some.config":   "val",
					},
				},
				Hooks:        nil,
				WindowConfig: window1,
				Assets:       nil,
			}
			details := scheduler.JobWithDetails{
				Job: &job,
				Schedule: &scheduler.Schedule{
					Interval: "0 * * * *",
				},
			}
			config := scheduler.RunConfig{
				Executor: scheduler.Executor{
					Name: "predator",
					Type: scheduler.ExecutorHook,
				},
				ScheduledAt: currentTime.Add(-time.Hour),
				JobRunID:    scheduler.JobRunID{},
			}

			tenantService := new(mockTenantService)
			tenantService.On("GetDetails", ctx, tnnt).Return(tenantDetails, nil)
			defer tenantService.AssertExpectations(t)

			interval, err := window.FromBaseWindow(w1).GetInterval(config.ScheduledAt)
			assert.NoError(t, err)
			executedAt := currentTime.Add(time.Hour)
			systemDefinedVars := map[string]string{
				"DSTART":          interval.Start.Format(time.RFC3339),
				"DEND":            interval.End.Format(time.RFC3339),
				"EXECUTION_TIME":  executedAt.Format(time.RFC3339),
				"JOB_DESTINATION": job.Destination,
			}
			taskContext := mock.Anything

			compiledFile := map[string]string{
				"someFileName": "fileContents",
			}
			assetCompiler := new(mockAssetCompiler)
			assetCompiler.On("CompileJobRunAssets", ctx, &job, systemDefinedVars, interval, taskContext).Return(compiledFile, nil)
			defer assetCompiler.AssertExpectations(t)

			templateCompiler := new(mockTemplateCompiler)
			templateCompiler.On("Compile", map[string]string{"some.config": "val"}, taskContext).
				Return(map[string]string{"some.config.compiled": "val.compiled"}, nil)
			templateCompiler.On("Compile", map[string]string{"secret.config": "a.secret.val"}, taskContext).
				Return(map[string]string{"secret.config.compiled": "a.secret.val.compiled"}, nil)
			defer templateCompiler.AssertExpectations(t)

			inputCompiler := service.NewJobInputCompiler(tenantService, templateCompiler, assetCompiler, logger)
			inputExecutorResp, err := inputCompiler.Compile(ctx, &details, config, executedAt)

			assert.NotNil(t, err)
			assert.Nil(t, inputExecutorResp)
			assert.ErrorContains(t, err, "hook:predator")
		})
	})
}

type mockTenantService struct {
	mock.Mock
}

func (m *mockTenantService) GetDetails(ctx context.Context, tnnt tenant.Tenant) (*tenant.WithDetails, error) {
	args := m.Called(ctx, tnnt)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*tenant.WithDetails), args.Error(1)
}

func (m *mockTenantService) GetSecrets(ctx context.Context, tnnt tenant.Tenant) ([]*tenant.PlainTextSecret, error) {
	args := m.Called(ctx, tnnt)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*tenant.PlainTextSecret), args.Error(1)
}

type mockAssetCompiler struct {
	mock.Mock
}

func (m *mockAssetCompiler) CompileJobRunAssets(ctx context.Context, job *scheduler.Job, systemEnvVars map[string]string, interval window.Interval, contextForTask map[string]interface{}) (map[string]string, error) {
	args := m.Called(ctx, job, systemEnvVars, interval, contextForTask)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]string), args.Error(1)
}

type mockTemplateCompiler struct {
	mock.Mock
}

func (m *mockTemplateCompiler) Compile(templateMap map[string]string, context map[string]any) (map[string]string, error) {
	args := m.Called(templateMap, context)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]string), args.Error(1)
}
