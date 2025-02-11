package service

import (
	"context"

	"github.com/goto/salt/log"

	"github.com/goto/optimus/core/scheduler"
	"github.com/goto/optimus/internal/lib/window"
	"github.com/goto/optimus/sdk/plugin"
)

const (
	typeEnv = "env"
)

type FilesCompiler interface {
	Compile(fileMap map[string]string, context map[string]any) (map[string]string, error)
}

type PluginRepo interface {
	GetByName(name string) (*plugin.Plugin, error)
}

type JobRunAssetsCompiler struct {
	compiler   FilesCompiler
	pluginRepo PluginRepo

	logger log.Logger
}

func NewJobAssetsCompiler(engine FilesCompiler, pluginRepo PluginRepo, logger log.Logger) *JobRunAssetsCompiler {
	return &JobRunAssetsCompiler{
		compiler:   engine,
		pluginRepo: pluginRepo,
		logger:     logger,
	}
}

func (c *JobRunAssetsCompiler) CompileJobRunAssets(ctx context.Context, job *scheduler.Job, systemEnvVars map[string]string, interval window.Interval, contextForTask map[string]interface{}) (map[string]string, error) {
	taskPlugin, err := c.pluginRepo.GetByName(job.Task.Name)
	if err != nil {
		c.logger.Error("error getting plugin [%s]: %s", job.Task.Name, err)
		return nil, err
	}

	inputFiles := job.Assets

	if taskPlugin.DependencyMod != nil {
		// check if task needs to override the compilation behaviour
		compiledAssetResponse, err := taskPlugin.DependencyMod.CompileAssets(ctx, plugin.CompileAssetsRequest{
			StartTime:    interval.Start,
			EndTime:      interval.End,
			Config:       toPluginConfig(job.Task.Config),
			Assets:       toPluginAssets(job.Assets),
			InstanceData: toJobRunSpecData(systemEnvVars),
		})
		if err != nil {
			c.logger.Error("error compiling assets through plugin dependency mod: %s", err)
			return nil, err
		}
		inputFiles = compiledAssetResponse.Assets.ToMap()
	}

	fileMap, err := c.compiler.Compile(inputFiles, contextForTask)
	if err != nil {
		c.logger.Error("error compiling assets: %s", err)
		return nil, err
	}
	return fileMap, nil
}

// TODO: deprecate after changing type for plugin
func toJobRunSpecData(mapping map[string]string) []plugin.JobRunSpecData {
	var jobRunData []plugin.JobRunSpecData
	for name, value := range mapping {
		jrData := plugin.JobRunSpecData{
			Name:  name,
			Value: value,
			Type:  typeEnv,
		}
		jobRunData = append(jobRunData, jrData)
	}
	return jobRunData
}

// TODO: deprecate
func toPluginAssets(assets map[string]string) plugin.Assets {
	var modelAssets plugin.Assets
	for name, val := range assets {
		pa := plugin.Asset{
			Name:  name,
			Value: val,
		}
		modelAssets = append(modelAssets, pa)
	}
	return modelAssets
}

// TODO: deprecate
func toPluginConfig(conf map[string]string) plugin.Configs {
	var pluginConfigs plugin.Configs
	for name, val := range conf {
		pc := plugin.Config{
			Name:  name,
			Value: val,
		}
		pluginConfigs = append(pluginConfigs, pc)
	}
	return pluginConfigs
}
