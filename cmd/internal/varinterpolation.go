package cmd

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"

	"code.cloudfoundry.org/quarks-operator/pkg/bosh/manifest"
	"code.cloudfoundry.org/quarks-utils/pkg/cmd"
	"code.cloudfoundry.org/quarks-utils/pkg/ctxlog"
	"code.cloudfoundry.org/quarks-utils/pkg/kubeconfig"
	"code.cloudfoundry.org/quarks-utils/pkg/logger"
)

const (
	vInterpolateFailedMessage = "variable-interpolation command failed."
)

// variableInterpolationCmd represents the variableInterpolation command
var variableInterpolationCmd = &cobra.Command{
	Use:   "variable-interpolation [flags]",
	Short: "Interpolate variables",
	Long: `Interpolate variables of a manifest:

This will interpolate all the variables from a folder and write an
interpolated manifest to STDOUT
`,
	PreRun: func(cmd *cobra.Command, args []string) {
		boshManifestFlagViperBind(cmd.Flags())
		outputFilePathFlagViperBind(cmd.Flags())
	},
	RunE: func(_ *cobra.Command, args []string) (err error) {
		defer func() {
			if err != nil {
				time.Sleep(debugGracePeriod)
			}
		}()

		namespace := viper.GetString("namespace")
		if len(namespace) == 0 {
			return errors.Errorf("persist-output command failed. namespace flag is empty.")
		}

		boshManifestPath, err := boshManifestFlagValidation()
		if err != nil {
			return errors.Wrap(err, vInterpolateFailedMessage)
		}
		outputFilePath, err := outputFilePathFlagValidation()
		if err != nil {
			return errors.Wrap(err, vInterpolateFailedMessage)
		}

		variablesDir := filepath.Clean(viper.GetString("variables-dir"))

		if _, err := os.Stat(boshManifestPath); os.IsNotExist(err) {
			return errors.Errorf("%s bosh-manifest-path file doesn't exist : %s", vInterpolateFailedMessage, boshManifestPath)
		}

		info, err := os.Stat(variablesDir)

		if os.IsNotExist(err) {
			return errors.Errorf("%s %s doesn't exist", vInterpolateFailedMessage, variablesDir)
		} else if err != nil {
			return errors.Errorf("%s Error on dir stat %s", vInterpolateFailedMessage, variablesDir)
		} else if !info.IsDir() {
			return errors.Errorf("%s %s is not a directory", vInterpolateFailedMessage, variablesDir)
		}

		// Read files
		boshManifestBytes, err := ioutil.ReadFile(boshManifestPath)
		if err != nil {
			return errors.Wrapf(err, "%s Reading file specified in the bosh-manifest-path flag failed", vInterpolateFailedMessage)
		}

		log = logger.NewControllerLogger(cmd.LogLevel())
		defer log.Sync()

		// Authenticate with the cluster
		clientSet, err := authenticateInCluster(log)
		if err != nil {
			return err
		}

		ctx := ctxlog.NewParentContext(log)

		return manifest.InterpolateFromSecretMounts(ctx, namespace, clientSet, boshManifestBytes, variablesDir, outputFilePath)
	},
}

func init() {
	utilCmd.AddCommand(variableInterpolationCmd)

	variableInterpolationCmd.Flags().String("namespace", "default", "namespace where persist output will run")
	variableInterpolationCmd.Flags().StringP("variables-dir", "v", "", "path to the variables dir")

	viper.BindPFlag("variables-dir", variableInterpolationCmd.Flags().Lookup("variables-dir"))
	viper.BindPFlag("namespace", variableInterpolationCmd.Flags().Lookup("namespace"))

	argToEnv := map[string]string{
		"variables-dir": "VARIABLES_DIR",
		"namespace":     "NAMESPACE",
	}

	pf := variableInterpolationCmd.Flags()
	boshManifestFlagCobraSet(pf, argToEnv)
	outputFilePathFlagCobraSet(pf, argToEnv)

	cmd.AddEnvToUsage(variableInterpolationCmd, argToEnv)
}

// authenticateInCluster authenticates with the in cluster and returns the client
func authenticateInCluster(log *zap.SugaredLogger) (*kubernetes.Clientset, error) {
	config, err := kubeconfig.NewGetter(log).Get("")
	if err != nil {
		return nil, errors.Wrapf(err, "Couldn't fetch Kubeconfig. Ensure kubeconfig is present to continue.")
	}
	if err := kubeconfig.NewChecker(log).Check(config); err != nil {
		return nil, errors.Wrapf(err, "Couldn't check Kubeconfig. Ensure kubeconfig is correct to continue.")
	}

	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create clientset with incluster config")
	}

	return clientSet, nil
}
