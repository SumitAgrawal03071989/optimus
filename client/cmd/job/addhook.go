package job

import (
	"github.com/goto/salt/log"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/goto/optimus/client/cmd/internal"
	"github.com/goto/optimus/client/cmd/internal/logger"
	"github.com/goto/optimus/client/cmd/internal/survey"
	"github.com/goto/optimus/client/local/specio"
	"github.com/goto/optimus/config"
	"github.com/goto/optimus/internal/models"
)

type addHookCommand struct {
	logger           log.Logger
	configFilePath   string
	clientConfig     *config.ClientConfig
	jobSurvey        *survey.JobSurvey
	jobAddHookSurvey *survey.JobAddHookSurvey
	namespaceSurvey  *survey.NamespaceSurvey
	pluginRepo       *models.PluginRepository
}

// NewAddHookCommand initializes command for adding hook
func NewAddHookCommand() *cobra.Command {
	l := logger.NewClientLogger()
	addHook := &addHookCommand{
		logger:           l,
		jobSurvey:        survey.NewJobSurvey(),
		jobAddHookSurvey: survey.NewJobAddHookSurvey(),
		namespaceSurvey:  survey.NewNamespaceSurvey(l),
	}
	cmd := &cobra.Command{
		Use:      "addhook",
		Aliases:  []string{"add_hook", "add-hook", "addHook", "attach_hook", "attach-hook", "attachHook"},
		Short:    "Attach a new Hook to existing job",
		Long:     "Add a runnable instance that will be triggered before or after the base transformation.",
		Example:  "optimus addhook",
		RunE:     addHook.RunE,
		PreRunE:  addHook.PreRunE,
		PostRunE: addHook.PostRunE,
	}
	// Config filepath flag
	cmd.Flags().StringVarP(&addHook.configFilePath, "config", "c", config.EmptyPath, "File path for client configuration")

	return cmd
}

func (a *addHookCommand) PreRunE(_ *cobra.Command, _ []string) error {
	// Load mandatory config
	conf, err := config.LoadClientConfig(a.configFilePath)
	if err != nil {
		return err
	}

	a.clientConfig = conf

	a.pluginRepo, err = internal.InitPlugins(config.LogLevel(a.logger.Level()))
	return err
}

func (a *addHookCommand) RunE(_ *cobra.Command, _ []string) error {
	namespace, err := a.namespaceSurvey.AskToSelectNamespace(a.clientConfig)
	if err != nil {
		return err
	}

	jobSpecReadWriter, err := specio.NewJobSpecReadWriter(afero.NewOsFs())
	if err != nil {
		return err
	}

	selectedJobName, err := a.jobSurvey.AskToSelectJobName(jobSpecReadWriter, namespace.Job.Path)
	if err != nil {
		return err
	}
	jobSpec, err := jobSpecReadWriter.ReadByName(namespace.Job.Path, selectedJobName)
	if err != nil {
		return err
	}
	newJobSpec, err := a.jobAddHookSurvey.AskToAddHook(a.pluginRepo, jobSpec)
	if err != nil {
		return err
	}
	if err := jobSpecReadWriter.Write(jobSpec.Path, newJobSpec); err != nil {
		return err
	}
	a.logger.Info("Hook successfully added to %s", selectedJobName)
	return nil
}

func (*addHookCommand) PostRunE(*cobra.Command, []string) error {
	internal.CleanupPlugins()
	return nil
}
