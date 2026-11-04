package outlook

// MAPI properties by schema name. Values read by schema name are UTC for
// dates (Date.UTC). Named (PidLid) properties use their property-set GUID
// and LID, never a store-specific 0x8xxx tag (PORTING §1).
const (
	tagPrefix    = "http://schemas.microsoft.com/mapi/proptag/"
	idPrefix     = "http://schemas.microsoft.com/mapi/id/"
	stringPrefix = "http://schemas.microsoft.com/mapi/string/"

	psetPublicStrings = "{00020329-0000-0000-C000-000000000046}"

	psetCommon = "{00062008-0000-0000-C000-000000000046}"
	psetTask   = "{00062003-0000-0000-C000-000000000046}"
)

// Message properties.
var (
	propLongTermEntryID   = tagPrefix + "0x66700102"
	propLastModified      = tagPrefix + "0x30080040"
	propMessageClass      = tagPrefix + "0x001A001F"
	propSubject           = tagPrefix + "0x0037001F"
	propSenderName        = tagPrefix + "0x0C1A001F"
	propSenderSMTP        = tagPrefix + "0x5D01001F"
	propSenderEmail       = tagPrefix + "0x0C1F001F"
	propSenderEntryID     = tagPrefix + "0x0C190102"
	propSentReprSMTP      = tagPrefix + "0x5D02001F"
	propSentReprEmail     = tagPrefix + "0x0065001F"
	propSentReprEntryID   = tagPrefix + "0x00410102"
	propToMe              = tagPrefix + "0x0057000B"
	propCcMe              = tagPrefix + "0x0058000B"
	propRecipientMe       = tagPrefix + "0x0059000B"
	propDeliveryTime      = tagPrefix + "0x0E060040"
	propSubmitTime        = tagPrefix + "0x00390040"
	propCreationTime      = tagPrefix + "0x30070040"
	propLastVerbTime      = tagPrefix + "0x10820040"
	propLastVerb          = tagPrefix + "0x10810003"
	propLocalCommitTime   = tagPrefix + "0x67090040"
	propMessageFlags      = tagPrefix + "0x0E070003"
	propHasAttach         = tagPrefix + "0x0E1B000B"
	propImportance        = tagPrefix + "0x00170003"
	propSensitivity       = tagPrefix + "0x00360003"
	propSize              = tagPrefix + "0x0E080003"
	propSpamConfidence    = tagPrefix + "0x40760003"
	propFlagStatus        = tagPrefix + "0x10900003"
	propReadReceipt       = tagPrefix + "0x0029000B"
	propInternetMessageID = tagPrefix + "0x1035001F"
	propInReplyTo         = tagPrefix + "0x1042001F"
	propConversationID    = tagPrefix + "0x30130102"
	propConversationIndex = tagPrefix + "0x00710102"
	propSearchKey         = tagPrefix + "0x300B0102"
	propBody              = tagPrefix + "0x1000001F"
	propTransportHeaders  = tagPrefix + "0x007D001F"
	propReferences        = tagPrefix + "0x1039001F"
	propReminderSet       = idPrefix + psetCommon + "/8503000B"
	propReminderTime      = idPrefix + psetCommon + "/85020040"
	propFlagRequest       = idPrefix + psetCommon + "/8530001F"
	propKeywords          = stringPrefix + psetPublicStrings + "/Keywords" // categories, multi-valued
	propTaskDueDate       = idPrefix + psetTask + "/81050040"
	propTaskStartDate     = idPrefix + psetTask + "/81040040"
	// Inline attachment identity and bytes (MS-OXPROPS 2.2.2.1).
	propAttachContentID       = tagPrefix + "0x3712001F"
	propAttachContentLocation = tagPrefix + "0x3713001F"
	propAttachMimeTag         = tagPrefix + "0x370E001F"
	propAttachFlags           = tagPrefix + "0x37140003"
	propAttachSize            = tagPrefix + "0x0E200003"
	propAttachDataBin         = tagPrefix + "0x37010102"
	propAttachLongFilename    = tagPrefix + "0x3707001F"
	propAttachFilename        = tagPrefix + "0x3704001F"
	builtinEntryID            = "EntryID"    // store-relative EntryID (hex string)
	builtinCategories         = "Categories" // comma-separated names
	builtinLastModifiedSort   = "[LastModificationTime]"
)

// Folder and store properties.
var (
	propEntryID         = tagPrefix + "0x0FFF0102" // hex = the object model's EntryID
	propContainerClass  = tagPrefix + "0x3613001F" // IPF.Note… for mail folders
	propSubfolders      = tagPrefix + "0x360A000B"
	propFolderType      = tagPrefix + "0x36010003" // 2 = search folder
	propAttrHidden      = tagPrefix + "0x10F4000B"
	propContentCount    = tagPrefix + "0x36020003"
	propContentUnread   = tagPrefix + "0x36030003" // moves on read/unread, which the modification time does not
	propCommitTimeMax   = tagPrefix + "0x670A0040" // PR_LOCAL_COMMIT_TIME_MAX
	propMailboxOwnerEID = tagPrefix + "0x661B0102"
)

// Recipient and address-entry properties.
var (
	propRecipientType = tagPrefix + "0x0C150003" // 1 to, 2 cc, 3 bcc
	propDisplayName   = tagPrefix + "0x3001001F"
	propCompanyName   = tagPrefix + "0x3A16001F"
	propDepartment    = tagPrefix + "0x3A18001F"
	propJobTitle      = tagPrefix + "0x3A17001F"
	// Contact phone numbers (PidTagBusinessTelephoneNumber and
	// PidTagMobileTelephoneNumber).
	propBusinessPhone = tagPrefix + "0x3A08001F"
	propMobilePhone   = tagPrefix + "0x3A1C001F"
	propEmail1        = "Email1Address"
	propEmail2        = "Email2Address"
	propEmail3        = "Email3Address"
	propEmailAddress  = tagPrefix + "0x3003001F"
	propAddrType      = tagPrefix + "0x3002001F"
	propSMTPAddress   = tagPrefix + "0x39FE001F"
	// PR_EMS_AB_PROXY_ADDRESSES (PidTagAddressBookProxyAddresses), PtypMultipleString
	propProxyAddresses = tagPrefix + "0x800F101F"
)

// Object-model enumerations (Outlook type library).
const (
	// OlExchangeStoreType
	olPrimaryExchangeMailbox    = 0
	olExchangeMailbox           = 1 // a delegate mailbox (shared mailboxes opened by automapping too)
	olExchangePublicFolder      = 2
	olNotExchange               = 3 // PST, IMAP, …
	olAdditionalExchangeMailbox = 4
	// OlDefaultFolders
	olFolderDeletedItems = 3
	olFolderSentMail     = 5
	olFolderInbox        = 6
	olFolderCalendar     = 9  // the default calendar (CalendarCollections)
	olFolderContacts     = 10 // the default address book (ContactCollections)
	olFolderDrafts       = 16
	olFolderJunk         = 23
	// MAPI recipient types (PR_RECIPIENT_TYPE)
	mapiTo  = 1
	mapiCc  = 2
	mapiBcc = 3
)

// MAPI constants.
const (
	folderTypeSearch = 2
	msgFlagRead      = 0x1
	olUserItems      = 0 // OlTableContents
	addrTypeExchange = "EX"
)
