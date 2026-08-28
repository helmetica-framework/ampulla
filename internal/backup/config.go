// Package backup resolves a BackupPolicy against the controller's defaults, and reads the
// bucket out of the Secret COSI populates.
package backup

import (
	"errors"
	"fmt"

	backupsv1 "github.com/helmetica-framework/ampulla/api/v1"
)

// Defaults are the cluster-wide settings the operator of the cluster configures on the
// controller. Every one of them can be overridden per policy.
type Defaults struct {
	// BucketClassName names the COSI BucketClass the backup buckets are provisioned from.
	BucketClassName string
	// BucketAccessClassName names the COSI BucketAccessClass the credentials are minted from.
	BucketAccessClassName string

	// Schedules are the cron expressions k8up runs each of its jobs on.
	Schedules backupsv1.ScheduleSpec
}

// DefaultDefaults are the fallbacks for everything an operator does not configure. There
// is deliberately no default bucket class: which object storage the backups land in is a
// decision about where a customer's data is kept, and guessing it is worse than refusing
// to back up.
//
// The schedules use k8up's `-random` variants, which spread the jobs of all services over
// the given window instead of starting every backup in the cluster at the same minute.
var DefaultDefaults = Defaults{
	Schedules: backupsv1.ScheduleSpec{
		Backup: "@daily-random",
		Prune:  "@weekly-random",
		Check:  "@weekly-random",
	},
}

// DefaultRetention applies when a policy asks for backups without saying how long to keep
// them. Every retention field being zero means "keep everything forever" to restic, which
// is never what an unconfigured policy means.
var DefaultRetention = backupsv1.Retention{KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6}

// Config is a policy with all defaults resolved.
type Config struct {
	Mode backupsv1.Mode

	Schedules backupsv1.ScheduleSpec

	BucketClassName       string
	BucketAccessClassName string

	Retention backupsv1.Retention
}

// Resolve fills a policy's empty fields from def.
//
// A policy that cannot be acted on - no bucket class, no schedule - is an error, because
// silently not backing up a service whose owner asked for backups is the one outcome this
// controller must never produce.
func Resolve(spec backupsv1.BackupPolicySpec, def Defaults) (Config, error) {
	cfg := Config{
		Mode: orDefault(spec.Mode, backupsv1.ModeSchedule),
		Schedules: backupsv1.ScheduleSpec{
			Backup: orDefault(spec.Schedule.Backup, def.Schedules.Backup),
			Prune:  orDefault(spec.Schedule.Prune, def.Schedules.Prune),
			Check:  orDefault(spec.Schedule.Check, def.Schedules.Check),
		},
		BucketClassName:       orDefault(spec.BucketClassName, def.BucketClassName),
		BucketAccessClassName: orDefault(spec.BucketAccessClassName, def.BucketAccessClassName),
		Retention:             spec.Retention,
	}
	if cfg.Retention.IsZero() {
		cfg.Retention = DefaultRetention
	}

	var errs []error
	if cfg.BucketClassName == "" {
		errs = append(errs, errors.New("no BucketClass: set .spec.bucketClassName or the controller's --default-bucket-class"))
	}
	if cfg.BucketAccessClassName == "" {
		errs = append(errs, errors.New("no BucketAccessClass: set .spec.bucketAccessClassName or the controller's --default-bucket-access-class"))
	}
	// A schedule is only ampulla's business in ModeSchedule. In ModeBucketOnly the service's
	// own operator decides when it backs up.
	if cfg.Mode == backupsv1.ModeSchedule && cfg.Schedules.Backup == "" {
		errs = append(errs, errors.New("no backup schedule: set .spec.schedule.backup or the controller's --default-schedule"))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("this BackupPolicy is not actionable: %w", err)
	}

	return cfg, nil
}

func orDefault[T ~string](value, fallback T) T {
	if value == "" {
		return fallback
	}
	return value
}
