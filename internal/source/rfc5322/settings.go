package rfc5322

// Settings the connector owns. A key spelled at its read sites drifts: a typo
// silently reads the schema default instead of the configured value, so every
// read goes through one of these names and the schema check in
// settings_test.go fails when one stops resolving. The six mail.* caps are
// owned by package mail (mail.Limits).
const (
	// SettingFolderRoles maps a folder path to its role ("<path>=<role>").
	SettingFolderRoles = "rfc5322.folder_roles"
	// SettingMaxMessageMB is the largest message file a sync reads.
	SettingMaxMessageMB = "rfc5322.max_message_mb"
	// SettingRawCharset decodes raw 8-bit header lines.
	SettingRawCharset = "rfc5322.headers.raw_charset"
	// SettingMimeMaxDepth bounds the nested multipart entities of one message.
	SettingMimeMaxDepth = "rfc5322.mime.max_depth"
)
