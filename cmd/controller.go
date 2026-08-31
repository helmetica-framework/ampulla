package cmd

import (
	"crypto/tls"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"go.uber.org/multierr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	backupsv1 "github.com/helmetica-framework/ampulla/api/v1"
	"github.com/helmetica-framework/ampulla/controllers"
	"github.com/helmetica-framework/ampulla/internal/backup"
)

var (
	metricsAddr          string
	enableLeaderElection bool
	probeAddr            string
	zapOpts              = zap.Options{
		Development: true,
	}
)

func init() {
	RootCmd.AddCommand(controllerCmd)

	zapFlagSet := flag.NewFlagSet("zap", flag.ExitOnError)
	zapOpts.BindFlags(zapFlagSet)
	controllerCmd.Flags().AddGoFlagSet(zapFlagSet)

	controllerCmd.Flags().StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	controllerCmd.Flags().StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	controllerCmd.Flags().BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	controllerCmd.Flags().Bool("metrics-secure", true, "If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	controllerCmd.Flags().String("metrics-cert-path", "", "The directory that contains the metrics server certificate.")
	controllerCmd.Flags().String("metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	controllerCmd.Flags().String("metrics-cert-key", "tls.key", "The name of the metrics server key file.")

	controllerCmd.Flags().String("default-bucket-class", "", "The COSI BucketClass backup buckets are provisioned from, for instances that name none themselves. Without it, and without .spec.backup.bucketClassName, an instance asking for backups is rejected.")
	controllerCmd.Flags().String("default-bucket-access-class", "", "The COSI BucketAccessClass the bucket credentials are minted from, for instances that name none themselves.")
	controllerCmd.Flags().String("default-schedule", backup.DefaultDefaults.Schedules.Backup, "The k8up schedule backups run on, for instances that set none themselves.")
	controllerCmd.Flags().String("default-prune-schedule", backup.DefaultDefaults.Schedules.Prune, "The k8up schedule old snapshots are pruned on. Empty disables pruning.")
	controllerCmd.Flags().String("default-check-schedule", backup.DefaultDefaults.Schedules.Check, "The k8up schedule the restic repository is checked on. Empty disables checks.")
}

var controllerCmd = &cobra.Command{
	Use:   "controller",
	Short: "Starts the controller manager",
	Long:  "Starts the controller manager",
	RunE:  runController,
}

func runController(cmd *cobra.Command, _ []string) error {
	secureMetrics, smerr := cmd.Flags().GetBool("metrics-secure")
	metricsCertPath, mcperr := cmd.Flags().GetString("metrics-cert-path")
	metricsCertName, mcnerr := cmd.Flags().GetString("metrics-cert-name")
	metricsCertKey, mckerr := cmd.Flags().GetString("metrics-cert-key")

	bucketClass, bcerr := cmd.Flags().GetString("default-bucket-class")
	bucketAccessClass, bacerr := cmd.Flags().GetString("default-bucket-access-class")
	schedule, scherr := cmd.Flags().GetString("default-schedule")
	pruneSchedule, pserr := cmd.Flags().GetString("default-prune-schedule")
	checkSchedule, cserr := cmd.Flags().GetString("default-check-schedule")

	if err := multierr.Combine(smerr, mcperr, mcnerr, mckerr, bcerr, bacerr, scherr, pserr, cserr); err != nil {
		return fmt.Errorf("failed to get flags: %w", err)
	}

	defaults := backup.Defaults{
		BucketClassName:       bucketClass,
		BucketAccessClassName: bucketAccessClass,
		Schedules: backupsv1.ScheduleSpec{
			Backup: schedule,
			Prune:  pruneSchedule,
			Check:  checkSchedule,
		},
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       []func(*tls.Config){},
	}
	if secureMetrics {
		// Protects the metrics endpoint with authn/authz, backed by the metrics-auth role
		// in config/rbac.
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	var metricsCertWatcher *certwatcher.CertWatcher
	if len(metricsCertPath) > 0 {
		cmd.Println("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(metricsCertPath, metricsCertName),
			filepath.Join(metricsCertPath, metricsCertKey),
		)
		if err != nil {
			return fmt.Errorf("failed to initialize metrics certificate watcher: %w", err)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 controllers.Scheme(),
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "ampulla.backups.helmetica.io",

		Cache: controllers.CacheOptions(),

		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		return fmt.Errorf("unable to start manager: %w", err)
	}

	bpm := controllers.BackupPolicyManager{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorder("backuppolicy-controller"),
		Log:       mgr.GetLogger().WithName("backuppolicy-controller"),
		APIReader: mgr.GetAPIReader(),
		Defaults:  defaults,
	}

	if err := bpm.SetupWithManager("backuppolicy", mgr); err != nil {
		return fmt.Errorf("unable to create BackupPolicy controller: %w", err)
	}

	if metricsCertWatcher != nil {
		cmd.Println("Adding metrics certificate watcher to manager")
		if err := mgr.Add(metricsCertWatcher); err != nil {
			return fmt.Errorf("unable to add metrics certificate watcher to manager: %w", err)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	cmd.Println("Starting the controller manager")
	if err := mgr.Start(cmd.Context()); err != nil {
		return fmt.Errorf("problem running manager: %w", err)
	}
	return nil
}
