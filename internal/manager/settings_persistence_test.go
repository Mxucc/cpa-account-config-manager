package manager

import "testing"

func TestMirroredSettingsSurvivePluginPrivateDirectoryReplacement(t *testing.T) {
	rawConfig := []byte(`
data_dir: old-plugin-data
default_policy:
  enabled: true
  apply_mode: missing
  scan_interval_seconds: 45
  priority: 7
  websockets: false
inspection_policy:
  enabled: true
  scan_interval_minutes: 15
  model_probe_enabled: true
  model_probe_full_sweep: true
  scan_manually_disabled: true
  model_probe_interval_minutes: 20
  model_probe_batch_size: 40
  model_probe_models:
    codex: gpt-5.4
    openai: gpt-5.4
    claude: claude-sonnet-4-5-20250929
    gemini: gemini-2.0-flash
    xai: grok-4
  failure_threshold: 4
  recovery_threshold: 3
  passive_circuit_enabled: true
  passive_failure_threshold: 6
  passive_failure_window_minutes: 120
  passive_circuit_minutes: 10
  auto_disable: true
  auto_enable: true
  auto_delete: false
  auto_delete_invalid_credentials: false
  delete_grace_hours: 168
  delete_batch_size: 20
  anomaly_trigger_enabled: true
  anomaly_threshold_percent: 60
  anomaly_minimum_accounts: 12
  anomaly_cooldown_minutes: 90
  anomaly_notification_enabled: true
  anomaly_notification_only: true
  anomaly_notification_url: https://notify.example/hook?available=${available_accounts}
  notification_available_accounts_enabled: true
  notification_available_accounts_threshold: 8
  notification_availability_percent_enabled: true
  notification_availability_percent_threshold: 35
  notification_cooldown_minutes: 45
update_policy:
  check_enabled: true
  check_interval_hours: 12
  auto_update: true
operation_settings:
  extended_history: true
`)
	config := ParseConfig(rawConfig)
	config.DataDir = t.TempDir()

	if config.InspectionPolicy == nil || !config.InspectionPolicy.Enabled || !config.InspectionPolicy.AutoDisable || !config.InspectionPolicy.AutoEnable || !config.InspectionPolicy.ModelProbeEnabled || !config.InspectionPolicy.AnomalyNotificationEnabled || !config.InspectionPolicy.AnomalyNotificationOnly {
		t.Fatalf("parsed inspection policy = %#v", config.InspectionPolicy)
	}
	if config.InspectionPolicy.AnomalyNotificationURL != "https://notify.example/hook?available=${available_accounts}" {
		t.Fatalf("parsed anomaly notification URL = %q", config.InspectionPolicy.AnomalyNotificationURL)
	}
	if !config.InspectionPolicy.NotificationAvailableEnabled || config.InspectionPolicy.NotificationAvailableBelow != 8 ||
		!config.InspectionPolicy.NotificationPercentEnabled || config.InspectionPolicy.NotificationPercentBelow != 35 ||
		config.InspectionPolicy.NotificationCooldownMinutes != 45 {
		t.Fatalf("parsed availability notification policy = %#v", config.InspectionPolicy)
	}
	if config.UpdatePolicy == nil || !config.UpdatePolicy.AutoUpdate || config.UpdatePolicy.CheckIntervalHours != 12 {
		t.Fatalf("parsed update policy = %#v", config.UpdatePolicy)
	}
	if config.OperationSettings == nil || !config.OperationSettings.ExtendedHistory {
		t.Fatalf("parsed operation settings = %#v", config.OperationSettings)
	}

	inspection := NewInspectionEngine(nil, nil, nil)
	inspection.Configure(config)
	t.Cleanup(inspection.Shutdown)
	gotInspection := inspection.Snapshot().Policy
	if !gotInspection.Enabled || !gotInspection.AutoDisable || !gotInspection.AutoEnable || !gotInspection.ModelProbeEnabled || !gotInspection.ModelProbeFullSweep || !gotInspection.ScanManuallyDisabled || !gotInspection.AnomalyNotificationEnabled || !gotInspection.AnomalyNotificationOnly {
		t.Fatalf("restored inspection policy = %#v", gotInspection)
	}
	if gotInspection.AnomalyNotificationURL != "https://notify.example/hook?available=${available_accounts}" {
		t.Fatalf("restored anomaly notification URL = %q", gotInspection.AnomalyNotificationURL)
	}
	if !gotInspection.NotificationAvailableEnabled || gotInspection.NotificationAvailableBelow != 8 ||
		!gotInspection.NotificationPercentEnabled || gotInspection.NotificationPercentBelow != 35 ||
		gotInspection.NotificationCooldownMinutes != 45 {
		t.Fatalf("restored availability notification policy = %#v", gotInspection)
	}
	if gotInspection.ScanIntervalMinutes != 15 || gotInspection.ModelProbeIntervalMinutes != 20 || gotInspection.ModelProbeBatchSize != 40 {
		t.Fatalf("restored inspection intervals = %#v", gotInspection)
	}

	updates := NewUpdateChecker("0.2.981")
	updates.Configure(config)
	gotUpdate := updates.Snapshot().Policy
	if !gotUpdate.CheckEnabled || !gotUpdate.AutoUpdate || gotUpdate.CheckIntervalHours != 12 {
		t.Fatalf("restored update policy = %#v", gotUpdate)
	}

	operations := NewOperationJournal()
	operations.Configure(config)
	if gotOperations := operations.RetentionSettings(); !gotOperations.ExtendedHistory {
		t.Fatalf("restored operation settings = %#v", gotOperations)
	}

	replacement := config
	replacement.DataDir = t.TempDir()
	reloadedInspection := NewInspectionEngine(nil, nil, nil)
	reloadedInspection.Configure(replacement)
	t.Cleanup(reloadedInspection.Shutdown)
	if got := reloadedInspection.Snapshot().Policy; got != gotInspection {
		t.Fatalf("inspection policy after private directory replacement = %#v, want %#v", got, gotInspection)
	}
	reloadedUpdates := NewUpdateChecker("0.2.982")
	reloadedUpdates.Configure(replacement)
	if got := reloadedUpdates.Snapshot().Policy; got != gotUpdate {
		t.Fatalf("update policy after private directory replacement = %#v, want %#v", got, gotUpdate)
	}
	reloadedOperations := NewOperationJournal()
	reloadedOperations.Configure(replacement)
	if got := reloadedOperations.RetentionSettings(); !got.ExtendedHistory {
		t.Fatalf("operation settings after private directory replacement = %#v", got)
	}
}

func TestPrivateSettingsRemainBackwardCompatibleWithoutMirroredConfig(t *testing.T) {
	dataDir := t.TempDir()
	inspectionPolicy := defaultInspectionPolicy()
	inspectionPolicy.Enabled = true
	inspectionPolicy.AutoDisable = true
	if errSave := saveInspectionState(inspectionStorePath(dataDir), persistedInspectionState{
		Version: inspectionStoreVersion,
		Policy:  inspectionPolicy,
		Records: map[string]inspectionRecord{},
	}); errSave != nil {
		t.Fatalf("save legacy inspection state: %v", errSave)
	}
	if errSave := saveUpdateState(updateStorePath(dataDir), persistedUpdateState{
		Version: updateStoreVersion,
		Policy:  UpdatePolicy{CheckEnabled: true, CheckIntervalHours: 48},
	}); errSave != nil {
		t.Fatalf("save legacy update state: %v", errSave)
	}

	config := Config{DataDir: dataDir}
	inspection := NewInspectionEngine(nil, nil, nil)
	inspection.Configure(config)
	t.Cleanup(inspection.Shutdown)
	if got := inspection.Snapshot().Policy; !got.Enabled || !got.AutoDisable {
		t.Fatalf("legacy inspection policy = %#v", got)
	}
	updates := NewUpdateChecker("0.2.981")
	updates.Configure(config)
	if got := updates.Snapshot().Policy; !got.CheckEnabled || got.CheckIntervalHours != 48 {
		t.Fatalf("legacy update policy = %#v", got)
	}
}

func TestCorrectedMirroredSettingsClearPriorConfigurationErrors(t *testing.T) {
	dataDir := t.TempDir()
	inspection := NewInspectionEngine(nil, nil, nil)
	inspection.Configure(Config{DataDir: dataDir})
	t.Cleanup(inspection.Shutdown)
	invalidInspection := defaultInspectionPolicy()
	invalidInspection.ScanIntervalMinutes = minInspectionInterval - 1
	inspection.Configure(Config{DataDir: dataDir, InspectionPolicy: &invalidInspection})
	if got := inspection.Snapshot().StorageError; got != "inspection state could not be loaded" {
		t.Fatalf("invalid inspection configuration error = %q", got)
	}
	validInspection := defaultInspectionPolicy()
	inspection.Configure(Config{DataDir: dataDir, InspectionPolicy: &validInspection})
	if got := inspection.Snapshot().StorageError; got != "" {
		t.Fatalf("corrected inspection configuration error = %q", got)
	}

	updates := NewUpdateChecker("0.2.981")
	updates.Configure(Config{DataDir: dataDir})
	invalidUpdate := UpdatePolicy{CheckEnabled: true, CheckIntervalHours: maxUpdateCheckIntervalHours + 1}
	updates.Configure(Config{DataDir: dataDir, UpdatePolicy: &invalidUpdate})
	if got := updates.Snapshot().Error; got != "update state could not be loaded" {
		t.Fatalf("invalid update configuration error = %q", got)
	}
	validUpdate := defaultUpdatePolicy()
	updates.Configure(Config{DataDir: dataDir, UpdatePolicy: &validUpdate})
	if got := updates.Snapshot().Error; got != "" {
		t.Fatalf("corrected update configuration error = %q", got)
	}
}
