package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	backupsv1 "github.com/helmetica-framework/ampulla/api/v1"
)

var testDefaults = Defaults{
	BucketClassName:       "backups",
	BucketAccessClassName: "backups",
	Schedules: backupsv1.ScheduleSpec{
		Backup: "@daily-random",
		Prune:  "@weekly-random",
		Check:  "@weekly-random",
	},
}

func TestResolve_Defaults(t *testing.T) {
	// What a chart renders when its values say no more than `backup.enabled: true`.
	cfg, err := Resolve(backupsv1.BackupPolicySpec{}, testDefaults)
	require.NoError(t, err)

	assert.Equal(t, Config{
		Mode: backupsv1.ModeSchedule,
		Schedules: backupsv1.ScheduleSpec{
			Backup: "@daily-random",
			Prune:  "@weekly-random",
			Check:  "@weekly-random",
		},
		BucketClassName:       "backups",
		BucketAccessClassName: "backups",
		Retention:             DefaultRetention,
	}, cfg, "an empty policy takes the cluster's defaults")
}

func TestResolve_Overrides(t *testing.T) {
	cfg, err := Resolve(backupsv1.BackupPolicySpec{
		Mode: backupsv1.ModeBucketOnly,
		Schedule: backupsv1.ScheduleSpec{
			Backup: "0 2 * * *",
			Prune:  "0 3 * * 0",
			Check:  "0 4 * * 0",
		},
		BucketClassName:       "backups-rma",
		BucketAccessClassName: "backups-rma",
		Retention:             backupsv1.Retention{KeepDaily: 14, KeepYearly: 2},
	}, testDefaults)
	require.NoError(t, err)

	assert.Equal(t, Config{
		Mode: backupsv1.ModeBucketOnly,
		Schedules: backupsv1.ScheduleSpec{
			Backup: "0 2 * * *",
			Prune:  "0 3 * * 0",
			Check:  "0 4 * * 0",
		},
		BucketClassName:       "backups-rma",
		BucketAccessClassName: "backups-rma",
		Retention:             backupsv1.Retention{KeepDaily: 14, KeepYearly: 2},
	}, cfg, "every default is overridable per policy")
}

func TestResolve_PartialRetentionIsNotCompleted(t *testing.T) {
	// Someone asking to keep 3 snapshots means 3, not 3 plus the default weeklies and
	// monthlies. The defaults only stand in for a retention nobody configured at all.
	cfg, err := Resolve(backupsv1.BackupPolicySpec{
		Retention: backupsv1.Retention{KeepLast: 3},
	}, testDefaults)
	require.NoError(t, err)

	assert.Equal(t, backupsv1.Retention{KeepLast: 3}, cfg.Retention)
}

func TestResolve_NotActionable(t *testing.T) {
	// Nothing here can produce a working backup, and quietly doing nothing would leave a
	// service whose owner asked for backups without any.
	_, err := Resolve(backupsv1.BackupPolicySpec{}, Defaults{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no BucketClass")
	assert.Contains(t, err.Error(), "no BucketAccessClass")
	assert.Contains(t, err.Error(), "no backup schedule")
}

func TestResolve_BucketOnlyNeedsNoSchedule(t *testing.T) {
	// A service that backs itself up needs the bucket, not a schedule - and not being able
	// to name one must not stop the bucket being provisioned.
	cfg, err := Resolve(
		backupsv1.BackupPolicySpec{Mode: backupsv1.ModeBucketOnly},
		Defaults{BucketClassName: "backups", BucketAccessClassName: "backups"},
	)
	require.NoError(t, err)

	assert.Equal(t, backupsv1.ModeBucketOnly, cfg.Mode)
	assert.Empty(t, cfg.Schedules.Backup)
}

func TestResolve_BucketOnlyStillNeedsAClass(t *testing.T) {
	_, err := Resolve(backupsv1.BackupPolicySpec{Mode: backupsv1.ModeBucketOnly}, Defaults{})
	require.ErrorContains(t, err, "no BucketClass")
	assert.NotContains(t, err.Error(), "no backup schedule", "a schedule is not ampulla's business in this mode")
}
