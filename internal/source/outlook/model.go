package outlook

// The Outlook object model as far as the connector uses it: the ONE list of
// members it may touch. The guard refuses anything else, which makes the
// connector read-only by construction (no Save, Delete, Move, Send,
// MarkAsTask, no property puts — Object has no put at all). Table, Columns
// and Sort only shape an in-memory view of a folder. Classes also key the
// Windows DISPID cache: a member's DISPID is looked up once per class.
//
// An item opened by EntryID may be a MailItem, ReportItem, MeetingItem, …
// whose DISPIDs are not guaranteed to agree, so items get classItem, which
// is never cached per class (only per object).

type member struct {
	call     bool   // method (Call) rather than property (Get)
	result   string // class of an object-valued result, "" for plain values
	nullable bool   // may return no object (Nothing); callers check for nil
}

const (
	classApplication  = "Application"
	classNamespace    = "Namespace"
	classStores       = "Stores"
	classStore        = "Store"
	classFolders      = "Folders"
	classFolder       = "Folder"
	classTable        = "Table"
	classColumns      = "Columns"
	classColumn       = "Column"
	classAccessor     = "PropertyAccessor"
	classItem         = "Item"
	classItems        = "Items"
	classAttachments  = "Attachments"
	classAttachment   = "Attachment"
	classRecipients   = "Recipients"
	classRecipient    = "Recipient"
	classAddressEntry = "AddressEntry"
	classExchangeUser = "ExchangeUser"
	classRecurrence   = "RecurrencePattern"
	classAccounts     = "Accounts"
	classAccount      = "Account"
)

var model = map[string]map[string]member{
	classApplication: {"GetNamespace": {call: true, result: classNamespace}, "Version": {}},
	classNamespace: {
		"Stores": {result: classStores}, "DefaultStore": {result: classStore, nullable: true},
		"CurrentUser":     {result: classRecipient},
		"GetFolderFromID": {call: true, result: classFolder}, "GetItemFromID": {call: true, result: classItem},
		"GetAddressEntryFromID": {call: true, result: classAddressEntry},
		"Accounts":              {result: classAccounts},
	},
	classAccounts: {"Count": {}, "Item": {call: true, result: classAccount}},
	classAccount:  {"SmtpAddress": {}, "DeliveryStore": {result: classStore, nullable: true}},
	classStores:   {"Count": {}, "Item": {call: true, result: classStore}},
	classStore: {
		"DisplayName": {}, "StoreID": {}, "ExchangeStoreType": {}, "IsCachedExchange": {}, "FilePath": {},
		"PropertyAccessor": {result: classAccessor},
		"GetRootFolder":    {call: true, result: classFolder}, "GetDefaultFolder": {call: true, result: classFolder},
	},
	classFolders: {"GetFirst": {call: true, result: classFolder, nullable: true}, "GetNext": {call: true, result: classFolder, nullable: true}},
	classFolder: {
		"Folders": {result: classFolders}, "EntryID": {}, "Name": {}, "PropertyAccessor": {result: classAccessor},
		"GetTable": {call: true, result: classTable}, "DefaultItemType": {},
		"Items": {result: classItems},
	},
	classItems: {
		"Count": {}, "IncludeRecurrences": {},
		"Sort":     {call: true},
		"Restrict": {call: true, result: classItems},
		"GetFirst": {call: true, result: classItem, nullable: true},
		"GetNext":  {call: true, result: classItem, nullable: true},
	},
	classTable:    {"Columns": {result: classColumns}, "Sort": {call: true}, "GetArray": {call: true}},
	classColumns:  {"RemoveAll": {call: true}, "Add": {call: true, result: classColumn}},
	classColumn:   {}, // Columns.Add returns the new column; it is only released
	classAccessor: {"GetProperties": {call: true}, "GetProperty": {call: true}},
	classItem: {
		"Body": {}, "HTMLBody": {}, "PropertyAccessor": {result: classAccessor}, "Recipients": {result: classRecipients},
		"Attachments": {result: classAttachments},
		"EntryID":     {}, "Subject": {}, "Location": {}, "Organizer": {}, "AllDayEvent": {},
		"GetRecurrencePattern": {call: true, result: classRecurrence},
		"Start":                {}, "End": {}, "BusyStatus": {}, "MeetingStatus": {}, "ResponseStatus": {},
		"IsRecurring": {}, "RecurrenceState": {}, "GlobalAppointmentID": {}, "CleanGlobalObjectId": {},
		"LastModificationTime": {}, "RequiredAttendees": {}, "OptionalAttendees": {}, "Resources": {},
		"DisplayName": {}, "CompanyName": {}, "Department": {}, "JobTitle": {},
		"Email1Address": {}, "Email2Address": {}, "Email3Address": {},
	},
	// An attachment can vanish between Count and Item (the source still
	// downloading it, a concurrent change): Outlook answers Nothing, which
	// the connector treats as a failed read, never as an absent attachment.
	classAttachments: {"Count": {}, "Item": {call: true, result: classAttachment, nullable: true}},
	classAttachment:  {"FileName": {}, "Size": {}, "Type": {}, "PropertyAccessor": {result: classAccessor}},
	classRecipients:  {"Count": {}, "Item": {call: true, result: classRecipient}},
	classRecipient: {
		"PropertyAccessor": {result: classAccessor}, "AddressEntry": {result: classAddressEntry, nullable: true},
		"Name": {}, "Address": {}, "Type": {}, "Resolve": {call: true},
	},
	classAddressEntry: {
		"GetExchangeUser": {call: true, result: classExchangeUser, nullable: true}, "PropertyAccessor": {result: classAccessor},
		"Name": {}, "Address": {},
	},
	classExchangeUser: {"PrimarySmtpAddress": {}, "Address": {}, "PropertyAccessor": {result: classAccessor}},
	// RecurrencePattern: the real object model has GetOccurrence(DateTime),
	// never GetOccurrences. The pattern is read once per master; GetOccurrence
	// then answers one instance (moved/canceled exceptions included) or says
	// the series has no appointment on that date.
	classRecurrence: {
		"GetOccurrence":  {call: true, result: classItem, nullable: true},
		"RecurrenceType": {}, "Interval": {}, "PatternStartDate": {}, "PatternEndDate": {},
		"NoEndDate": {}, "Occurrences": {}, "StartTime": {},
		"DayOfWeekMask": {}, "DayOfMonth": {}, "Instance": {}, "MonthOfYear": {},
	},
}

// writable is the one exception to the read-only rule: an in-memory view
// flag that changes which rows Outlook lists, never stored data.
var writable = map[string]map[string]bool{
	classItems: {"IncludeRecurrences": true},
}

// lookup returns the allowed member of a class.
func lookup(class, name string, call bool) (member, bool) {
	m, ok := model[class][name]
	return m, ok && m.call == call
}

// cacheable reports whether DISPIDs of a class may be shared by every
// object of the class.
func cacheable(class string) bool { return class != classItem }
