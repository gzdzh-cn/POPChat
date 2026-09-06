package chat

import (
	"strings"
	"time"
)

const MaxNicknameLength = 10

// NormalizeNickname is the single boundary for local and remote profile data.
// Count runes instead of bytes so Chinese and other multi-byte characters are
// treated as one visible character.
func NormalizeNickname(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > MaxNicknameLength {
		runes = runes[:MaxNicknameLength]
	}
	return string(runes)
}

const (
	ProtocolName         = "dzhgo"
	ProtocolMajor        = 2
	ProtocolMinor        = 0
	DiscoveryPort        = 39190
	DiscoveryMagic       = "DZHGO_DISCOVERY_V1"
	PeerRelation         = "friend"
	DiscoveredState      = "discovered"
	DiscoveryScopePublic = "public"
	DiscoveryScopeFriend = "friend"
)

type ProtocolDialect struct {
	Name  string
	Magic string
	Major int
}

var protocolDialects = []ProtocolDialect{
	{Name: ProtocolName, Magic: DiscoveryMagic, Major: ProtocolMajor},
}

type Profile struct {
	Nickname               string `json:"nickname"`
	AvatarPath             string `json:"avatarPath"`
	AvatarData             string `json:"avatarData,omitempty"`
	AvatarHash             string `json:"avatarHash,omitempty"`
	AvatarVersion          int64  `json:"avatarVersion,omitempty"`
	Discoverable           bool   `json:"discoverable"`
	AutoSave               bool   `json:"autoSave"`
	FileSavePath           string `json:"fileSavePath"`
	SharedRootPath         string `json:"sharedRootPath"`
	SharedEnabled          bool   `json:"sharedEnabled"`
	SharedDriveMultiWindow bool   `json:"sharedDriveMultiWindow"`
	ShowHiddenFiles        bool   `json:"showHiddenFiles"`
	DirectoryOpenMode      string `json:"directoryOpenMode"`
	Theme                  string `json:"theme"`
	LaunchAtStartup        bool   `json:"launchAtStartup"`
}

type DeviceInfo struct {
	Platform               string `json:"platform"`
	OSVersion              string `json:"osVersion"`
	DeviceID               string `json:"deviceId"`
	FeiqID                 string `json:"feiqId,omitempty"`
	PublicKeyPEM           string `json:"publicKeyPem"`
	CertificateFingerprint string `json:"certificateFingerprint"`
	IP                     string `json:"ip"`
	Port                   int    `json:"port"`
	IdentityStatus         string `json:"identityStatus,omitempty"`
	ProtocolName           string `json:"protocolName"`
	ProtocolMajor          int    `json:"protocolMajor"`
}

type Peer struct {
	DeviceID               string    `json:"deviceId"`
	Nickname               string    `json:"nickname"`
	AvatarPath             string    `json:"avatarPath"`
	AvatarData             string    `json:"avatarData,omitempty"`
	AvatarHash             string    `json:"avatarHash,omitempty"`
	AvatarVersion          int64     `json:"avatarVersion,omitempty"`
	Platform               string    `json:"platform"`
	OSVersion              string    `json:"osVersion"`
	IP                     string    `json:"ip"`
	Port                   int       `json:"port"`
	PublicKeyPEM           string    `json:"publicKeyPem"`
	CertificateFingerprint string    `json:"certificateFingerprint"`
	Relation               string    `json:"relation"`
	Remark                 string    `json:"remark"`
	ProtocolName           string    `json:"protocolName,omitempty"`
	ProtocolMajor          int       `json:"protocolMajor,omitempty"`
	DiscoveryMagic         string    `json:"discoveryMagic,omitempty"`
	Capabilities           []string  `json:"capabilities,omitempty"`
	DiscoveryVisible       bool      `json:"discoveryVisible"`
	VisibleInFriends       bool      `json:"visibleInFriends"`
	RelationshipVersion    string    `json:"relationshipVersion,omitempty"`
	FriendshipState        string    `json:"friendshipState,omitempty"`
	Online                 bool      `json:"online"`
	LastSeen               string    `json:"lastSeen"`
	UpdatedAt              time.Time `json:"updatedAt"`
}

type FriendRequest struct {
	RequestID  string `json:"requestId"`
	DeviceID   string `json:"deviceId"`
	Nickname   string `json:"nickname"`
	Message    string `json:"message"`
	Status     string `json:"status"`
	Direction  string `json:"direction"`
	CreatedAt  string `json:"createdAt"`
	AcceptedAt string `json:"acceptedAt,omitempty"`
	UpdatedAt  string `json:"updatedAt"`
}

type Conversation struct {
	ConversationID string `json:"conversationId"`
	PeerDeviceID   string `json:"peerDeviceId"`
	LastMessage    string `json:"lastMessage"`
	LastMessageAt  string `json:"lastMessageAt"`
	UnreadCount    int    `json:"unreadCount"`
	Pinned         bool   `json:"pinned"`
}

type Message struct {
	MessageID               string `json:"messageId"`
	ConversationID          string `json:"conversationId"`
	SenderDeviceID          string `json:"senderDeviceId"`
	Kind                    string `json:"kind"`
	Content                 string `json:"content"`
	Status                  string `json:"status"`
	CreatedAt               string `json:"createdAt"`
	AttachmentID            string `json:"attachmentId,omitempty"`
	AttachmentName          string `json:"attachmentName,omitempty"`
	AttachmentSize          int64  `json:"attachmentSize,omitempty"`
	AttachmentMime          string `json:"attachmentMime,omitempty"`
	AttachmentThumbnail     string `json:"attachmentThumbnail,omitempty"`
	AttachmentThumbnailMime string `json:"attachmentThumbnailMime,omitempty"`
	AttachmentStatus        string `json:"attachmentStatus,omitempty"`
	AttachmentPath          string `json:"attachmentPath,omitempty"`
	IsFavorite              bool   `json:"isFavorite,omitempty"`
	DeletedAt               string `json:"deletedAt,omitempty"`
	QuoteMessageID          string `json:"quoteMessageId,omitempty"`
	QuoteContent            string `json:"quoteContent,omitempty"`
	ForwardedFrom           string `json:"forwardedFrom,omitempty"`
}

type AttachmentDetails struct {
	AttachmentID string `json:"attachmentId"`
	FileName     string `json:"fileName"`
	MimeType     string `json:"mimeType"`
	FileSize     int64  `json:"fileSize"`
	SHA256       string `json:"sha256"`
	Status       string `json:"status"`
	CreatedAt    string `json:"createdAt"`
	LocalPath    string `json:"localPath"`
}

type Attachment struct {
	AttachmentID  string `json:"attachmentId"`
	MessageID     string `json:"messageId"`
	FileName      string `json:"fileName"`
	MimeType      string `json:"mimeType"`
	FileSize      int64  `json:"fileSize"`
	SHA256        string `json:"sha256"`
	ThumbnailData string `json:"thumbnailData,omitempty"`
	ThumbnailMime string `json:"thumbnailMime,omitempty"`
	LocalPath     string `json:"localPath"`
	Status        string `json:"status"`
}

type ClearConversationResult struct {
	DeletedMessages      int `json:"deletedMessages"`
	DeletedAttachments   int `json:"deletedAttachments"`
	DeletedFiles         int `json:"deletedFiles"`
	SkippedExternalFiles int `json:"skippedExternalFiles"`
}

type AttachmentMigrationResult struct {
	SourceRoot   string `json:"sourceRoot"`
	TargetRoot   string `json:"targetRoot"`
	Total        int    `json:"total"`
	Migrated     int    `json:"migrated"`
	Skipped      int    `json:"skipped"`
	Failed       int    `json:"failed"`
	Unclassified int    `json:"unclassified"`
	Completed    bool   `json:"completed"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

type AttachmentMigrationProgress struct {
	Phase        string `json:"phase"`
	SourceRoot   string `json:"sourceRoot"`
	TargetRoot   string `json:"targetRoot"`
	Current      int    `json:"current"`
	Total        int    `json:"total"`
	FileName     string `json:"fileName,omitempty"`
	PeerDeviceID string `json:"peerDeviceId,omitempty"`
	Migrated     int    `json:"migrated"`
	Skipped      int    `json:"skipped"`
	Failed       int    `json:"failed"`
	Unclassified int    `json:"unclassified"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

type NetworkStatus struct {
	Status        string   `json:"status"`
	Interfaces    []string `json:"interfaces"`
	LocalIPs      []string `json:"localIps"`
	DiscoveryPort int      `json:"discoveryPort"`
	ChatPort      int      `json:"chatPort"`
	PeerCount     int      `json:"peerCount"`
	OnlineCount   int      `json:"onlineCount"`
	LastScanAt    string   `json:"lastScanAt"`
	LastError     string   `json:"lastError"`
}

type SharedFolderSettings struct {
	Enabled  bool   `json:"enabled"`
	RootPath string `json:"rootPath"`
}

// SharedFolder is the local management and remote discovery representation of
// one configured shared directory. RootPath is omitted from JSON when empty,
// which is how friend responses avoid exposing the owner's filesystem path.
type SharedFolder struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	RootPath     string `json:"rootPath,omitempty"`
	FileCount    int    `json:"fileCount"`
	FolderCount  int    `json:"folderCount"`
	StatsReady   bool   `json:"statsReady"`
	StatsLoading bool   `json:"statsLoading"`
	UpdatedAt    string `json:"updatedAt"`
}

type SharedFolderStatus struct {
	SharedFolderSettings
	Folders        []SharedFolder `json:"folders"`
	FileCount      int            `json:"fileCount"`
	FolderCount    int            `json:"folderCount"`
	AvailableBytes uint64         `json:"availableBytes"`
	UpdatedAt      string         `json:"updatedAt"`
	StatsLoading   bool           `json:"statsLoading"`
	StatsReady     bool           `json:"statsReady"`
	StatsUpdatedAt string         `json:"statsUpdatedAt,omitempty"`
}

type SharedEntry struct {
	EntryID      string `json:"entryId"`
	Name         string `json:"name"`
	RelativePath string `json:"relativePath"`
	IsDirectory  bool   `json:"isDirectory"`
	Size         int64  `json:"size"`
	MimeType     string `json:"mimeType"`
	ModifiedAt   string `json:"modifiedAt"`
	SHA256       string `json:"sha256,omitempty"`
}

type SharedThumbnailRequest struct {
	RelativePath   string `json:"relativePath"`
	SharedFolderID string `json:"sharedFolderId,omitempty"`
	EntryID        string `json:"entryId,omitempty"`
	FileSize       int64  `json:"fileSize,omitempty"`
	ModifiedAt     string `json:"modifiedAt,omitempty"`
}

type SharedThumbnailResult struct {
	SharedFolderID string `json:"sharedFolderId,omitempty"`
	RelativePath   string `json:"relativePath"`
	Status         string `json:"status"`
	MimeType       string `json:"mimeType,omitempty"`
	ThumbnailMime  string `json:"thumbnailMime,omitempty"`
	Payload        string `json:"payload,omitempty"`
	Error          string `json:"error,omitempty"`
}

// SharedEntriesPage keeps large shared folders responsive. Offsets refer to
// the directory enumeration rather than an in-memory, fully sorted listing.
// That lets peers render the first page without waiting for every entry.
type SharedEntriesPage struct {
	Entries    []SharedEntry `json:"entries"`
	NextOffset int           `json:"nextOffset,omitempty"`
	HasMore    bool          `json:"hasMore,omitempty"`
}

type SharedTransfer struct {
	TransferID     string `json:"transferId"`
	DeviceID       string `json:"deviceId"`
	SharedFolderID string `json:"sharedFolderId,omitempty"`
	RelativePath   string `json:"relativePath"`
	FileName       string `json:"fileName"`
	FileSize       int64  `json:"fileSize"`
	Transferred    int64  `json:"transferred"`
	Status         string `json:"status"`
	Direction      string `json:"direction"`
	TargetPath     string `json:"targetPath"`
	ErrorMessage   string `json:"errorMessage,omitempty"`
}

type DiagnosticItem struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Advice string `json:"advice"`
}

type DiagnosticResult struct {
	Status    string           `json:"status"`
	Items     []DiagnosticItem `json:"items"`
	CreatedAt string           `json:"createdAt"`
}

type wireMessage struct {
	Magic          string `json:"magic,omitempty"`
	Type           string `json:"type"`
	Protocol       string `json:"protocol,omitempty"`
	Major          int    `json:"major,omitempty"`
	Minor          int    `json:"minor,omitempty"`
	MinMajor       int    `json:"minMajor,omitempty"`
	MinMinor       int    `json:"minMinor,omitempty"`
	RequestID      string `json:"requestId,omitempty"`
	DiscoveryScope string `json:"discoveryScope,omitempty"`
	MessageID      string `json:"messageId,omitempty"`
	DeviceID       string `json:"deviceId,omitempty"`
	Nickname       string `json:"nickname,omitempty"`
	AvatarHash     string `json:"avatarHash,omitempty"`
	AvatarVersion  int64  `json:"avatarVersion,omitempty"`
	AvatarData     string `json:"avatarData,omitempty"`
	AvatarMime     string `json:"avatarMime,omitempty"`
	// Avatar preview bytes are deliberately separate from AvatarData. Discovery
	// packets may carry a small, safe-to-cache preview while AvatarData remains
	// reserved for the authenticated full-avatar response.
	AvatarPreviewData   string                   `json:"avatarPreviewData,omitempty"`
	AvatarPreviewHash   string                   `json:"avatarPreviewHash,omitempty"`
	AvatarPreviewMime   string                   `json:"avatarPreviewMime,omitempty"`
	Platform            string                   `json:"platform,omitempty"`
	OSVersion           string                   `json:"osVersion,omitempty"`
	IP                  string                   `json:"ip,omitempty"`
	Port                int                      `json:"port,omitempty"`
	PublicKey           string                   `json:"publicKey,omitempty"`
	CertFP              string                   `json:"certificateFingerprint,omitempty"`
	Content             string                   `json:"content,omitempty"`
	QuoteMessageID      string                   `json:"quoteMessageId,omitempty"`
	QuoteContent        string                   `json:"quoteContent,omitempty"`
	ForwardedFrom       string                   `json:"forwardedFrom,omitempty"`
	Kind                string                   `json:"kind,omitempty"`
	Status              string                   `json:"status,omitempty"`
	FileName            string                   `json:"fileName,omitempty"`
	MimeType            string                   `json:"mimeType,omitempty"`
	ThumbnailData       string                   `json:"thumbnailData,omitempty"`
	ThumbnailMime       string                   `json:"thumbnailMime,omitempty"`
	FileSize            int64                    `json:"fileSize,omitempty"`
	SHA256              string                   `json:"sha256,omitempty"`
	AttachmentID        string                   `json:"attachmentId,omitempty"`
	MessageIDs          []string                 `json:"messageIds,omitempty"`
	ChunkIndex          int                      `json:"chunkIndex,omitempty"`
	ChunkSize           int                      `json:"chunkSize,omitempty"`
	WindowSize          int                      `json:"windowSize,omitempty"`
	WindowID            int                      `json:"windowId,omitempty"`
	WindowBytes         int64                    `json:"windowBytes,omitempty"`
	AckTargetBytes      int64                    `json:"ackTargetBytes,omitempty"`
	AckCumulative       bool                     `json:"ackCumulative,omitempty"`
	DiskWriteMs         int64                    `json:"diskWriteMs,omitempty"`
	TransferMode        string                   `json:"transferMode,omitempty"`
	Transferred         int64                    `json:"transferred,omitempty"`
	Payload             string                   `json:"payload,omitempty"`
	Capabilities        []string                 `json:"capabilities,omitempty"`
	SyncSince           string                   `json:"syncSince,omitempty"`
	SyncUntil           string                   `json:"syncUntil,omitempty"`
	SyncToken           string                   `json:"syncToken,omitempty"`
	ReadAt              string                   `json:"readAt,omitempty"`
	Probe               bool                     `json:"probe,omitempty"`
	AcceptedAt          string                   `json:"acceptedAt,omitempty"`
	TargetDeviceID      string                   `json:"targetDeviceId,omitempty"`
	SourceDeviceID      string                   `json:"sourceDeviceId,omitempty"`
	SourcePublicKey     string                   `json:"sourcePublicKey,omitempty"`
	RestoreVersion      int                      `json:"restoreVersion,omitempty"`
	RestoreSignature    string                   `json:"restoreSignature,omitempty"`
	Reason              string                   `json:"reason,omitempty"`
	FriendshipState     string                   `json:"friendshipState,omitempty"`
	RelationshipVersion string                   `json:"relationshipVersion,omitempty"`
	RemovedAt           string                   `json:"removedAt,omitempty"`
	AvailableBytes      int64                    `json:"availableBytes,omitempty"`
	RequiredBytes       int64                    `json:"requiredBytes,omitempty"`
	RelativePath        string                   `json:"relativePath,omitempty"`
	SharedFolderID      string                   `json:"sharedFolderId,omitempty"`
	TransferID          string                   `json:"transferId,omitempty"`
	TransferToken       string                   `json:"transferToken,omitempty"`
	StreamID            int                      `json:"streamId,omitempty"`
	StreamCount         int                      `json:"streamCount,omitempty"`
	ActiveStreams       int                      `json:"activeStreams,omitempty"`
	StreamOffset        int64                    `json:"streamOffset,omitempty"`
	StreamLength        int64                    `json:"streamLength,omitempty"`
	StreamBytes         int64                    `json:"streamBytes,omitempty"`
	Offset              int64                    `json:"offset,omitempty"`
	Entries             []SharedEntry            `json:"entries,omitempty"`
	SharedFolders       []SharedFolder           `json:"sharedFolders,omitempty"`
	ListOffset          int                      `json:"listOffset,omitempty"`
	ListLimit           int                      `json:"listLimit,omitempty"`
	ShowHiddenFiles     bool                     `json:"showHiddenFiles,omitempty"`
	NextOffset          int                      `json:"nextOffset,omitempty"`
	HasMore             bool                     `json:"hasMore,omitempty"`
	ThumbnailRequests   []SharedThumbnailRequest `json:"thumbnailRequests,omitempty"`
	ThumbnailResults    []SharedThumbnailResult  `json:"thumbnailResults,omitempty"`
}

func protocolDialectForMessage(message wireMessage) (ProtocolDialect, bool) {
	for _, dialect := range protocolDialects {
		if message.Protocol != dialect.Name || message.Magic != dialect.Magic || message.Major != dialect.Major {
			continue
		}
		if message.MinMajor > 0 && dialect.Major < message.MinMajor {
			continue
		}
		return dialect, true
	}
	return ProtocolDialect{}, false
}

func protocolDialectsForPeer(_ Peer) []ProtocolDialect {
	return append([]ProtocolDialect(nil), protocolDialects...)
}

func hasProtocolCapability(dialect ProtocolDialect, capabilities []string, capability string) bool {
	if hasCapability(capabilities, capability) {
		return true
	}
	return dialect.Major >= ProtocolMajor && capability == "text"
}
