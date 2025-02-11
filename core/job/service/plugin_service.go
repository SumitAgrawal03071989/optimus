package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/goto/salt/log"
	"golang.org/x/net/context"

	"github.com/goto/optimus/core/job"
	"github.com/goto/optimus/core/tenant"
	"github.com/goto/optimus/internal/compiler"
	"github.com/goto/optimus/internal/lib/window"
	"github.com/goto/optimus/sdk/plugin"
)

const (
	projectConfigPrefix = "GLOBAL__"

	configKeyDstart        = "DSTART"
	configKeyDend          = "DEND"
	configKeyExecutionTime = "EXECUTION_TIME"
	configKeyDestination   = "JOB_DESTINATION"

	TimeISOFormat = time.RFC3339
)

var (
	ErrUpstreamModNotFound = errors.New("upstream mod not found for plugin")
	ErrYamlModNotExist     = errors.New("yaml mod not found for plugin")
)

type PluginRepo interface {
	GetByName(string) (*plugin.Plugin, error)
}

type Engine interface {
	Compile(templateMap map[string]string, context map[string]any) (map[string]string, error)
	CompileString(input string, context map[string]any) (string, error)
}

type JobPluginService struct {
	pluginRepo PluginRepo
	engine     Engine

	now func() time.Time

	logger log.Logger
}

func NewJobPluginService(pluginRepo PluginRepo, engine Engine, logger log.Logger) *JobPluginService {
	return &JobPluginService{pluginRepo: pluginRepo, engine: engine, logger: logger, now: time.Now}
}

func (p JobPluginService) Info(_ context.Context, taskName job.TaskName) (*plugin.Info, error) {
	taskPlugin, err := p.pluginRepo.GetByName(taskName.String())
	if err != nil {
		p.logger.Error("error getting plugin [%s]: %s", taskName.String(), err)
		return nil, err
	}

	if taskPlugin.YamlMod == nil {
		p.logger.Error("task plugin yaml mod is not found")
		return nil, ErrYamlModNotExist
	}

	return taskPlugin.YamlMod.PluginInfo(), nil
}

func (p JobPluginService) GenerateDestination(ctx context.Context, tnnt *tenant.WithDetails, task job.Task) (job.ResourceURN, error) {
	taskPlugin, err := p.pluginRepo.GetByName(task.Name().String())
	if err != nil {
		p.logger.Error("error getting plugin [%s]: %s", task.Name().String(), err)
		return "", err
	}

	if taskPlugin.DependencyMod == nil {
		p.logger.Error(ErrUpstreamModNotFound.Error())
		return "", ErrUpstreamModNotFound
	}

	compiledConfig := p.compileConfig(task.Config().Map(), tnnt)

	destination, err := taskPlugin.DependencyMod.GenerateDestination(ctx, plugin.GenerateDestinationRequest{
		Config: compiledConfig,
	})
	if err != nil {
		p.logger.Error("error generating destination: %s", err)
		return "", fmt.Errorf("failed to generate destination: %w", err)
	}

	return job.ResourceURN(destination.URN()), nil
}

func (p JobPluginService) GenerateUpstreams(ctx context.Context, jobTenant *tenant.WithDetails, spec *job.Spec, dryRun bool) ([]job.ResourceURN, error) {
	taskPlugin, err := p.pluginRepo.GetByName(spec.Task().Name().String())
	if err != nil {
		p.logger.Error("error getting plugin [%s]: %s", spec.Task().Name().String(), err)
		return nil, err
	}

	if taskPlugin.DependencyMod == nil {
		p.logger.Error(ErrUpstreamModNotFound.Error())
		return nil, ErrUpstreamModNotFound
	}

	w, err := getWindow(jobTenant, spec)
	if err != nil {
		return nil, err
	}

	assets, err := p.compileAsset(ctx, taskPlugin, spec, w, p.now())
	if err != nil {
		p.logger.Error("error compiling asset: %s", err)
		return nil, fmt.Errorf("asset compilation failure: %w", err)
	}

	compiledConfigs := p.compileConfig(spec.Task().Config(), jobTenant)

	resp, err := taskPlugin.DependencyMod.GenerateDependencies(ctx, plugin.GenerateDependenciesRequest{
		Config: compiledConfigs,
		Assets: plugin.AssetsFromMap(assets),
		Options: plugin.Options{
			DryRun: dryRun,
		},
	})
	if err != nil {
		p.logger.Error("error generating dependencies: %s", err)
		return nil, err
	}

	var upstreamURNs []job.ResourceURN
	for _, dependency := range resp.Dependencies {
		resourceURN := job.ResourceURN(dependency)
		upstreamURNs = append(upstreamURNs, resourceURN)
	}

	return upstreamURNs, nil
}

func (p JobPluginService) compileConfig(configs job.Config, tnnt *tenant.WithDetails) plugin.Configs {
	tmplCtx := compiler.PrepareContext(
		compiler.From(tnnt.GetConfigs()).WithName("proj").WithKeyPrefix(projectConfigPrefix),
		compiler.From(tnnt.SecretsMap()).WithName("secret"),
	)

	var pluginConfigs plugin.Configs
	for key, val := range configs {
		compiledConf, err := p.engine.CompileString(val, tmplCtx)
		if err != nil {
			p.logger.Warn("template compilation encountered suppressed error: %s", err.Error())
			compiledConf = val
		}
		pluginConfigs = append(pluginConfigs, plugin.Config{
			Name:  key,
			Value: compiledConf,
		})
	}
	return pluginConfigs
}

func (p JobPluginService) compileAsset(ctx context.Context, taskPlugin *plugin.Plugin, spec *job.Spec, w window.Window, scheduledAt time.Time) (map[string]string, error) {
	var jobDestination string
	if taskPlugin.DependencyMod != nil {
		var assets map[string]string
		if spec.Asset() != nil {
			assets = spec.Asset()
		}
		jobDestinationResponse, err := taskPlugin.DependencyMod.GenerateDestination(ctx, plugin.GenerateDestinationRequest{
			Config: plugin.ConfigsFromMap(spec.Task().Config()),
			Assets: plugin.AssetsFromMap(assets),
			Options: plugin.Options{
				DryRun: true,
			},
		})
		if err != nil {
			p.logger.Error("error generating destination: %s", err)
			return nil, err
		}
		jobDestination = jobDestinationResponse.Destination
	}

	interval, err := w.GetInterval(scheduledAt)
	if err != nil {
		return nil, err
	}

	var assets map[string]string
	if spec.Asset() != nil {
		assets = spec.Asset()
	}

	templates, err := p.engine.Compile(assets, map[string]interface{}{
		configKeyDstart:        interval.Start.Format(TimeISOFormat),
		configKeyDend:          interval.End.Format(TimeISOFormat),
		configKeyExecutionTime: scheduledAt.Format(TimeISOFormat),
		configKeyDestination:   jobDestination,
	})
	if err != nil {
		p.logger.Error("error compiling asset: %s", err)
		return nil, fmt.Errorf("failed to compile templates: %w", err)
	}

	return templates, nil
}

func getWindow(jobTenant *tenant.WithDetails, spec *job.Spec) (window.Window, error) {
	return window.From(spec.WindowConfig(), spec.Schedule().Interval(), jobTenant.Project().GetPreset)
}
