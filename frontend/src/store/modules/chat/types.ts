export interface Profile {
  nickname: string
  avatarPath: string
  avatarData?: string
  avatarHash?: string
  avatarVersion?: number
  discoverable: boolean
  autoSave: boolean
  fileSavePath: string
  theme: string
  launchAtStartup: boolean
  sharedRootPath?: string
  sharedEnabled?: boolean
  sharedDriveMultiWindow?: boolean
  showHiddenFiles?: boolean
  directoryOpenMode?: 'single' | 'double' | string
}

export interface Peer {
  deviceId: string
  nickname: string
  avatarPath: string
  avatarData?: string
  avatarHash?: string
  avatarVersion?: number
  platform: string
  osVersion: string
  ip: string
  port: number
  publicKeyPem: string
  certificateFingerprint: string
  relation: string
  remark: string
  protocolName?: string
  protocolMajor?: number
  discoveryMagic?: string
  capabilities?: string[]
  discoveryVisible: boolean
  visibleInFriends?: boolean
  friendshipState?: string
  online: boolean
  lastSeen: string
}

export interface FriendRequest {
  requestId: string
  deviceId: string
  nickname: string
  message: string
  status: string
  direction: string
  createdAt: string
  acceptedAt?: string
  updatedAt: string
  attachmentId?: string
  attachmentName?: string
  attachmentSize?: number
  attachmentMime?: string
  attachmentStatus?: string
}

export interface Conversation {
  conversationId: string
  peerDeviceId: string
  lastMessage: string
  lastMessageAt: string
  unreadCount: number
  pinned: boolean
}

export interface Message {
  messageId: string
  conversationId: string
  senderDeviceId: string
  kind: string
  content: string
  status: string
  createdAt: string
  attachmentId?: string
  attachmentName?: string
  attachmentSize?: number
  attachmentMime?: string
  attachmentThumbnail?: string
  attachmentThumbnailMime?: string
  attachmentStatus?: string
  attachmentPath?: string
  isFavorite?: boolean
  deletedAt?: string
  quoteMessageId?: string
  quoteContent?: string
  forwardedFrom?: string
}

export interface AttachmentDetails {
  attachmentId: string
  fileName: string
  mimeType: string
  fileSize: number
  sha256: string
  status: string
  createdAt: string
  localPath: string
}

export interface TransferProgress {
  messageId?: string
  attachmentId: string
  peerDeviceId?: string
  transferred: number
  total: number
  percent: number
  speed?: number
  averageSpeed?: number
  peakSpeed?: number
  rawSpeed?: number
  etaSeconds?: number
  elapsedMs?: number
  chunkSize?: number
  windowSize?: number
  windowBytes?: number
  windowThroughput?: number
  inFlightBytes?: number
  ackTargetBytes?: number
  socketWriteMs?: number
  ackWaitMs?: number
  confirmedThroughput?: number
  ackLatencyMs?: number
  diskWriteMs?: number
  transferMode?: 'parallel-binary' | 'binary-window' | 'json-window' | 'legacy-chunk' | string
  streamCount?: number
  activeStreams?: number
  streamId?: number
  streamOffset?: number
  streamLength?: number
  transport?: string
  protocol?: string
  tuningState?: 'probing' | 'accelerating' | 'stable' | 'backing_off' | string
  tuningReason?: string
  updatedAt?: string
  verified?: boolean
  sent?: number
  received?: number
  remoteReceived?: number
  direction: 'send' | 'receive' | 'remote-receive'
  phase: 'awaiting_acceptance' | 'transferring' | 'receiving' | 'completed' | 'canceled' | 'rejected' | 'failed' | string
}

export type TransferProgressByDirection = Partial<Record<TransferProgress['direction'], TransferProgress>>

export interface NetworkStatus {
  status: string
  interfaces: string[]
  localIps: string[]
  discoveryPort: number
  chatPort: number
  peerCount: number
  onlineCount: number
  lastScanAt: string
  lastError?: string
}

export interface AttachmentMigrationProgress {
  phase: string
  sourceRoot: string
  targetRoot: string
  current: number
  total: number
  fileName?: string
  peerDeviceId?: string
  migrated: number
  skipped: number
  failed: number
  unclassified: number
  errorMessage?: string
}
