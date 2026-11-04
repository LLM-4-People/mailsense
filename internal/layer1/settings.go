package layer1

// Setting keys layer 1 reads (internal/config/schema.json holds their
// types, defaults and bounds).
const (
	SettingAfterRuns        = "layer1.after_runs"
	SettingWorkers          = "layer1.workers"
	SettingShardRows        = "layer1.shard_rows"
	SettingTeachersFile     = "layer1.teachers.file"
	SettingRequirementsFile = "layer1.requirements.file"
	SettingUseEmbedding     = "layer1.features.embedding"
	SettingUseEvidence      = "layer1.features.evidence"
	SettingDimensions       = "layer1.embedding.dimensions"
	SettingScoreHead        = "layer1.heads.score"
	SettingGrid             = "layer1.l2.grid_log10"
	SettingDefaultL2        = "layer1.l2.default_log10"
	SettingMemory           = "layer1.lbfgs.memory"
	SettingMaxIterations    = "layer1.lbfgs.max_iterations"
	SettingTolerance        = "layer1.lbfgs.tolerance"
	SettingFolds            = "layer1.cv.folds"
	SettingKNN              = "layer1.knn.enabled"
	SettingKNNK             = "layer1.knn.k"
	SettingKNNTemperature   = "layer1.knn.temperature"
	SettingKNNPrior         = "layer1.knn.prior_strength"
	SettingCalibration      = "layer1.calibration.method"
	SettingCalibrationPrior = "layer1.calibration.prior_labels"
	SettingCertifyFraction  = "layer1.certify.fraction"
	SettingKeepRetired      = "layer1.retain.retired_models"
	SettingRetrainLabels    = "layer1.retrain.min_new_labels"
	SettingRetrainEvery     = "layer1.retrain.min_interval"
	SettingMaxNLLRegression = "layer1.promote.max_nll_regression"
)

// settingKeys are every layer-1 setting: a run's version key digests their
// values, and a change of one of them starts a run.
var settingKeys = []string{SettingWorkers, SettingShardRows, SettingTeachersFile, SettingRequirementsFile, SettingUseEmbedding,
	SettingUseEvidence, SettingDimensions, SettingScoreHead, SettingGrid, SettingDefaultL2, SettingMemory, SettingMaxIterations, SettingTolerance,
	SettingFolds, SettingKNN, SettingKNNK, SettingKNNTemperature, SettingKNNPrior, SettingCalibration, SettingCalibrationPrior,
	SettingCertifyFraction, SettingRetrainLabels, SettingMaxNLLRegression}
