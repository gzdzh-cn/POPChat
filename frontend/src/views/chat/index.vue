<template>
  <div class="chat-app" :class="{ 'theme-dark': isDark, 'is-mac': isMac, 'is-windows': !isMac }" @contextmenu="handleAppContextMenu">
    <div v-if="isMac" class="window-drag-region" aria-hidden="true"></div>
    <div v-if="isMac" class="mac-window-controls" aria-label="macOS 窗口控制">
      <button type="button" class="mac-control close" title="关闭" @click.stop="closeWindow"></button>
      <button type="button" class="mac-control minimise" title="最小化" @click.stop="minimiseWindow"></button>
      <button type="button" class="mac-control maximise" title="最大化" @click.stop="toggleMaximise"></button>
    </div>
    <aside class="rail" :class="{ 'is-locked': store.attachmentMigration.active }">
      <button class="profile-button" :class="{ active: section === 'settings' }" @click="selfAvatarPreviewVisible = true" @keydown.enter.space.prevent="selfAvatarPreviewVisible = true" :disabled="store.attachmentMigration.active" aria-label="放大我的头像" title="放大头像">
        <div class="avatar large" :style="avatarStyle(store.profile.nickname, store.profile.avatarData)">{{ store.profile.avatarData ? '' : initials(store.profile.nickname) }}</div>
      </button>
      <nav class="rail-nav">
        <button :class="{ active: section === 'friends' }" @click="enterFriends" :disabled="store.attachmentMigration.active" aria-label="好友"><icon-user-group /><small>好友</small><b v-if="totalUnreadCount" class="rail-unread-badge">{{ unreadLabel(totalUnreadCount) }}</b></button>
        <button :class="{ active: section === 'discover' }" @click="openDiscover" :disabled="store.attachmentMigration.active" aria-label="发现"><icon-search /><small>发现</small><b v-if="store.pendingRequests.length">{{ store.pendingRequests.length }}</b></button>
        <button :class="{ active: section === 'favorites' }" @click="openFavorites" :disabled="store.attachmentMigration.active" aria-label="收藏"><icon-bookmark /><small>收藏</small></button>
        <button :class="{ active: section === 'shared' }" @click="openSharedDrive" :disabled="store.attachmentMigration.active" aria-label="共享"><icon-cloud /><small>共享</small></button>
      </nav>
      <button class="rail-settings" :class="{ active: section === 'settings' }" @click="openSettings('general')" :disabled="store.attachmentMigration.active" aria-label="设置"><icon-settings /><small>设置</small></button>
    </aside>
    <Transition name="peer-info">
      <aside v-if="showPeerInfo && activePeer" class="info-pane info-overlay" @click.stop>
        <div class="info-head"><strong>好友资料</strong></div>
        <div class="info-profile"><div class="avatar huge" :style="avatarStyle(activePeer.nickname, activePeer.avatarData)">{{ activePeer.avatarData ? '' : initials(activePeer.nickname) }}</div><h3 class="nickname-ellipsis">{{ activePeer.remark || activePeer.nickname }}</h3><span>{{ activePeer.online ? '在线' : '离线' }}</span></div>
        <div class="info-fields"><label>设备类型<strong>{{ activePeer.platform }} · {{ activePeer.osVersion }}</strong></label><label>通讯协议<strong>{{ activePeer.protocolName || '未知' }}<template v-if="activePeer.protocolMajor">/{{ activePeer.protocolMajor }}.0</template></strong></label><label>备注<input v-model="peerRemark" @keyup.enter="savePeerRemark" @blur="savePeerRemark" /></label><label>IP 地址<strong>{{ activePeer.ip || '未知' }}:{{ activePeer.port || '-' }}</strong></label><label>设备 ID<strong class="mono">{{ activePeer.deviceId }}</strong></label><label>证书指纹<strong class="mono">{{ activePeer.certificateFingerprint || '未知' }}</strong></label><label>最近在线<strong>{{ formatLastSeen(activePeer.lastSeen) }}</strong></label></div>
        <div class="info-danger"><a-button status="danger" long :loading="clearingConversation" @click="clearCurrentConversation">清除聊天记录</a-button><span>只清除本机消息和接收的附件，不会删除好友关系。</span></div>
      </aside>
    </Transition>
    <a-modal v-model:visible="selfAvatarPreviewVisible" title="我的资料" :footer="false" modal-class="avatar-preview-modal">
      <div class="self-profile-card">
        <div class="self-profile-heading">
          <div class="avatar huge" :style="avatarStyle(store.profile.nickname, store.profile.avatarData)">{{ store.profile.avatarData ? '' : initials(store.profile.nickname) }}</div>
          <div class="self-profile-name"><strong class="nickname-ellipsis">{{ store.profile.nickname || '新用户' }}</strong><span>{{ deviceInfo?.platform || '桌面端' }} · {{ store.profile.discoverable ? '允许被发现' : '未开启发现' }}</span></div>
        </div>
        <div class="self-profile-body">
          <div class="profile-qr-box"><img v-if="myQRCode" :src="myQRCode" alt="我的飞秋Pro二维码" /><div v-else class="profile-qr-loading">二维码生成中</div></div>
          <div class="self-profile-fields">
            <div><span>飞秋号</span><strong>{{ deviceInfo?.feiqId || feiqID }}</strong></div>
            <div><span>设备平台</span><strong>{{ deviceInfo?.platform || '未知' }}</strong></div>
            <div><span>操作系统</span><strong>{{ deviceInfo?.osVersion || '未知' }}</strong></div>
            <div><span>设备 ID</span><strong class="mono">{{ deviceInfo?.deviceId || '尚未生成' }}</strong></div>
          </div>
        </div>
        <p class="self-profile-hint">扫一扫二维码，可以在局域网内发现并添加我</p>
      </div>
    </a-modal>

    <section v-show="section === 'friends'" class="workspace">
      <aside class="list-pane" :style="{ width: `${friendsWidth}px`, flexBasis: `${friendsWidth}px` }">
        <div class="pane-title friend-pane-title"><a-input v-model="friendSearch" class="friend-search" placeholder="搜索好友" allow-clear size="small"><template #prefix><icon-search /></template></a-input><button class="icon-button" @click="openDiscover" title="发现好友"><icon-plus /></button></div>
        <div class="list-scroll" @scroll="closeAllContextMenus">
          <button v-for="peer in filteredFriends" v-memo="[peer.deviceId, store.activePeerId, peer.remark, peer.nickname, peer.avatarData, peer.platform, peer.osVersion, peer.online, peer.friendshipState, peer.relation, conversationForPeer(peer.deviceId)?.lastMessage, conversationForPeer(peer.deviceId)?.lastMessageAt, conversationForPeer(peer.deviceId)?.unreadCount, conversationForPeer(peer.deviceId)?.pinned]" :key="peer.deviceId" class="peer-row" :class="{ selected: store.activePeerId === peer.deviceId, pinned: conversationForPeer(peer.deviceId)?.pinned }" @click="selectPeer(peer)" @contextmenu.prevent.stop="openPeerMenu($event, peer)">
            <div class="avatar" :style="avatarStyle(peer.nickname, peer.avatarData)">{{ peer.avatarData ? '' : initials(peer.nickname) }}<i :class="{ online: peer.online }" /></div>
            <div class="peer-copy"><strong><span class="nickname-ellipsis">{{ peer.remark || peer.nickname }}</span><i v-if="conversationForPeer(peer.deviceId)?.pinned" class="pin-mark">置顶</i></strong><span class="peer-device">{{ peerDeviceLabel(peer) }}</span><span class="peer-preview">{{ peer.friendshipState === 'removed' || peer.relation === 'removed' ? '不是好友' : (conversationForPeer(peer.deviceId)?.lastMessage || (peer.online ? '在线' : '离线')) }}</span></div><div v-if="conversationForPeer(peer.deviceId)?.lastMessageAt || unreadCount(peer.deviceId)" class="peer-meta"><time v-if="conversationForPeer(peer.deviceId)?.lastMessageAt" class="peer-time">{{ formatTime(conversationForPeer(peer.deviceId)?.lastMessageAt || '') }}</time><b v-if="unreadCount(peer.deviceId)" class="unread-badge">{{ unreadLabel(unreadCount(peer.deviceId)) }}</b></div>
          </button>
          <div v-if="!filteredFriends.length" class="empty-small"><div class="empty-icon">⌁</div><p>还没有好友</p><a-button type="primary" size="small" @click="openDiscover">去发现好友</a-button></div>
        </div>
      </aside>
      <div class="vertical-resizer" @pointerdown="startResize('friends', $event)" title="调整列表宽度" />
        <main class="conversation" v-if="activePeer" :style="{ '--composer-height': `${composerTotalHeight}px` }">
          <header class="conversation-head">
          <div class="head-peer"><strong class="nickname-ellipsis">{{ activePeer.remark || activePeer.nickname }}</strong><span class="head-status" :class="{ onlineText: activePeer.online }"><i :class="{ online: activePeer.online }" />{{ activePeer.online ? '在线' : '离线' }} · {{ activePeer.platform }}</span></div>
          <a-button type="text" aria-label="好友资料" title="好友资料" @pointerdown.prevent.stop="togglePeerInfo" @keydown.enter.space.prevent="togglePeerInfo"><icon-more /></a-button>
        </header>
        <div class="message-scroll" ref="messageScroll" @scroll="onMessageScroll(); closeAllContextMenus()" @wheel="cancelAutoScroll" @pointerdown="handleMessageAreaPointerDown" @touchstart="handleMessageAreaPointerDown" @click="handleMessageAreaClick">
          <div v-if="!activeMessages.length" class="conversation-empty"><div class="empty-icon">✦</div><h3>开始聊天</h3><p>向 <span class="nickname-ellipsis-inline">{{ activePeer.remark || activePeer.nickname }}</span> 发送第一条消息</p></div>
          <div v-for="message in activeMessages" v-memo="[message.messageId, message.kind, message.senderDeviceId, message.createdAt, message.content, message.quoteContent, message.status, message.isFavorite, message.attachmentId, message.attachmentMime, message.attachmentStatus, message.attachmentPath, message.attachmentThumbnail, message.attachmentSize, message.attachmentName, messagePreviews[message.messageId], selectedMessageIds.has(message.messageId), transferProgressFor(message)?.phase, transferProgressFor(message)?.transferred, transferProgressFor(message)?.speed, transferProgressFor(message)?.elapsedMs, transferProgressFor(message)?.etaSeconds, transferProgressFor(message)?.fileSize, attachmentActionBusy(message), activePeer?.deviceId, activePeer?.nickname, activePeer?.avatarData, store.profile.nickname, store.profile.avatarData]" :key="message.messageId" class="message-line" :class="{ mine: message.senderDeviceId === deviceInfo?.deviceId, 'is-selected': selectedMessageIds.has(message.messageId) }">
            <button v-if="message.senderDeviceId !== deviceInfo?.deviceId" type="button" class="avatar message-avatar avatar-button" :style="avatarStyle(activePeer.nickname, activePeer.avatarData)" aria-label="查看好友资料" title="查看好友资料" @click.stop="openPeerInfo">{{ activePeer.avatarData ? '' : initials(activePeer.nickname) }}</button>
            <button v-if="message.senderDeviceId === deviceInfo?.deviceId && (message.kind === 'file' || message.kind === 'text') && message.status === 'failed'" type="button" class="message-retry" :disabled="retryingMessages[message.messageId]" aria-label="重发消息" title="发送失败，点击重发" @click.stop="retryMessage(message)">!</button>
            <div class="message-bubble" :class="{ 'text-bubble': message.kind !== 'file', 'attachment-bubble': message.kind === 'file', 'image-attachment-bubble': message.kind === 'file' && isImageMessage(message), 'is-favorite': message.isFavorite }" @contextmenu.prevent.stop="openMessageMenu($event, message)">
              <div v-if="message.quoteContent" class="message-quote">{{ message.quoteContent }}</div>
              <template v-if="message.kind === 'file'">
                <template v-if="isImageMessage(message)">
                  <button class="image-message" :class="{ 'is-transferring': imageTransferActive(message) }" :aria-busy="imageTransferActive(message)" @click="openImage(message)" @dblclick.stop.prevent="openImage(message)">
                    <img v-if="messagePreviews[message.messageId]" :src="messagePreviews[message.messageId]" />
                    <span v-else class="image-pending-placeholder">图片 {{ message.attachmentName || message.content }}</span>
                    <div v-if="imageTransferActive(message)" class="image-transfer-mask"><span class="image-progress-ring" :style="imageProgressRingStyle(message)"><strong>{{ transferProgressPercent(message) }}%</strong></span><span>{{ transferProgressLabel(message) }}</span><a-button class="image-transfer-cancel" size="mini" status="danger" :loading="attachmentActionBusy(message)" @click.stop.prevent="cancelAttachment(message)">取消</a-button></div>
                  </button>
                  <div v-if="attachmentNeedsDecision(message)" class="attachment-actions">
                    <a-button size="mini" type="primary" :loading="attachmentActionBusy(message)" @click.stop.prevent="acceptAttachment(message)">接收</a-button>
                    <a-button size="mini" :loading="attachmentActionBusy(message)" @click.stop.prevent="saveAttachmentAs(message)">另存</a-button>
                    <a-button size="mini" status="danger" :loading="attachmentActionBusy(message)" @click.stop.prevent="rejectAttachment(message)">拒绝</a-button>
                  </div>
                  <div v-if="attachmentAwaitingAcceptance(message)" class="attachment-pending"><span class="attachment-pending-actions"><button type="button" class="transfer-details-button" @click.stop.prevent="showAttachmentDetails(message)">详情</button><a-button size="mini" status="danger" :loading="attachmentActionBusy(message)" @click.stop.prevent="cancelAttachment(message)">取消</a-button></span></div>
                </template>
                <template v-else>
                  <div class="file-attachment-content">
                    <strong class="file-attachment-title"><icon-file /> <span>{{ message.attachmentName || message.content }}</span></strong>
                    <span class="attachment-meta">{{ formatBytes(message.attachmentSize || 0) }}<template v-if="transferProgressFor(message)"> · <b class="attachment-percent">{{ transferProgressPercent(message) }}%</b></template></span>
                  </div>
                  <div v-if="attachmentNeedsDecision(message)" class="attachment-actions">
                    <a-button size="mini" type="primary" :loading="attachmentActionBusy(message)" @click.stop.prevent="acceptAttachment(message)">接收</a-button>
                    <a-button size="mini" :loading="attachmentActionBusy(message)" @click.stop.prevent="saveAttachmentAs(message)">另存</a-button>
                    <a-button size="mini" status="danger" :loading="attachmentActionBusy(message)" @click.stop.prevent="rejectAttachment(message)">拒绝</a-button>
                  </div>
                  <div v-if="attachmentAwaitingAcceptance(message)" class="attachment-pending"><span class="attachment-pending-actions"><button type="button" class="transfer-details-button" @click.stop.prevent="showAttachmentDetails(message)">详情</button><a-button size="mini" status="danger" :loading="attachmentActionBusy(message)" @click.stop.prevent="cancelAttachment(message)">取消</a-button></span></div>
                </template>
                <div v-if="transferProgressFor(message) && !['awaiting_acceptance', 'completed', 'failed', 'canceled', 'rejected'].includes(transferProgressFor(message)?.phase) && !isImageMessage(message)" class="transfer-progress"><div class="transfer-progress-head"><span class="transfer-progress-speed">{{ transferSpeedLabel(message) }}</span><button type="button" class="transfer-details-button" @click.stop.prevent="showAttachmentDetails(message)">详情</button><a-button size="mini" status="danger" :loading="attachmentActionBusy(message)" @click.stop.prevent="cancelAttachment(message)">取消</a-button></div><div class="transfer-progress-track"><i :style="{ width: `${transferProgressPercent(message)}%` }" /></div><div class="transfer-progress-foot"><span>已用 {{ transferElapsedLabel(message) }}</span><span>剩余 {{ transferEtaLabel(message) }}</span></div></div>
                <div v-if="attachmentCompletedLocal(message)" class="attachment-complete-actions"><button type="button" @click.stop="isImageMessage(message) ? openImage(message) : openAttachment(message)">打开</button><button type="button" @click.stop="revealAttachment(message)">打开文件夹</button><button type="button" @click.stop.prevent="showAttachmentDetails(message)">详情</button></div>
                <div v-if="transferProgressFor(message) && ((isImageMessage(message) && !attachmentCompletedLocal(message)) || ['awaiting_acceptance', 'failed', 'canceled', 'rejected'].includes(transferProgressFor(message)?.phase))" class="attachment-transfer-details-action"><button type="button" @click.stop.prevent="showAttachmentDetails(message)">查看传输详情</button></div>
              </template>
              <template v-else>{{ message.content }}</template>
              <small>{{ formatTime(message.createdAt) }}<template v-if="messageStatusText(message.status, message.kind, message.attachmentStatus, message.senderDeviceId === deviceInfo?.deviceId) && (message.kind === 'file' || message.senderDeviceId === deviceInfo?.deviceId)"> <span class="message-status" :class="{ rejected: (message.attachmentStatus || message.status) === 'rejected' }">{{ messageStatusText(message.status, message.kind, message.attachmentStatus, message.senderDeviceId === deviceInfo?.deviceId) }}</span></template></small>
            </div>
            <div v-if="message.senderDeviceId === deviceInfo?.deviceId" class="avatar message-avatar" :style="avatarStyle(store.profile.nickname, store.profile.avatarData)">{{ store.profile.avatarData ? '' : initials(store.profile.nickname) }}</div>
          </div>
        </div>
        <div v-if="messageMenu.visible" class="message-context-menu" :style="messageMenuStyle" @click.stop>
          <template v-if="messageMenu.message && messageMenu.message.kind !== 'file'">
            <button @click="copyTextMessage(messageMenu.message)">复制</button><button @click="forwardMessage(messageMenu.message)">转发</button><button @click="toggleFavorite(messageMenu.message)">{{ messageMenu.message.isFavorite ? '取消收藏' : '收藏' }}</button><button @click="enterMultiSelect(messageMenu.message)">多选</button><button @click="quoteMessage(messageMenu.message)">引用</button><button class="danger" @click="deleteMessage(messageMenu.message)">删除</button>
          </template>
          <template v-else-if="messageMenu.message && isImageMessage(messageMenu.message)">
            <button @click="forwardMessage(messageMenu.message)">转发</button><button @click="toggleFavorite(messageMenu.message)">{{ messageMenu.message.isFavorite ? '取消收藏' : '收藏' }}</button><button :disabled="!attachmentHasLocalFile(messageMenu.message)" @click="copyImageMessage(messageMenu.message)">复制</button><button :disabled="!attachmentHasLocalFile(messageMenu.message)" @click="saveAttachmentCopy(messageMenu.message)">另存为</button><button :disabled="!attachmentHasLocalFile(messageMenu.message)" @click="openImage(messageMenu.message)">打开</button><button :disabled="!attachmentHasLocalFile(messageMenu.message)" @click="revealAttachment(messageMenu.message)">打开所在文件夹</button>
          </template>
          <template v-else-if="messageMenu.message">
            <button @click="forwardMessage(messageMenu.message)">转发</button><button @click="toggleFavorite(messageMenu.message)">{{ messageMenu.message.isFavorite ? '取消收藏' : '收藏' }}</button><button :disabled="!attachmentHasLocalFile(messageMenu.message)" @click="saveAttachmentCopy(messageMenu.message)">另存为</button><button :disabled="!attachmentHasLocalFile(messageMenu.message)" @click="openAttachment(messageMenu.message)">打开</button><button :disabled="!attachmentHasLocalFile(messageMenu.message)" @click="revealAttachment(messageMenu.message)">打开所在文件夹</button>
          </template>
        </div>
        <div v-if="selectionMode" class="selection-toolbar"><strong>已选 {{ selectedMessageIds.size }} 条</strong><a-button size="small" @click="batchFavorite">收藏</a-button><a-button size="small" @click="batchForward">转发</a-button><a-button size="small" status="danger" @click="batchDelete">删除</a-button><a-button size="small" @click="exitMultiSelect">取消</a-button></div>
        <a-modal v-model:visible="attachmentDetailsVisible" title="附件传输详情" :footer="false" :width="'min(680px, calc(100vw - 32px))'" modal-class="attachment-details-modal" :mask="true" :render-to-body="true" :mask-style="{ backgroundColor: isDark ? 'rgba(0, 0, 0, .36)' : 'rgba(20, 32, 52, .18)', backdropFilter: 'blur(1px)' }">
          <div v-if="attachmentDetails" class="attachment-details">
            <div class="attachment-details-hero">
              <div class="attachment-details-hero-heading">
                <strong class="attachment-details-name" :title="attachmentDetails.fileName">{{ attachmentDetails.fileName }}</strong>
              </div>
            </div>
            <div class="attachment-details-section">
              <h4>状态</h4>
              <div class="attachment-details-grid">
                <p><span>当前速度</span><strong class="attachment-details-rate"><span>{{ detailProgressSpeed.primary }}</span><small v-if="detailProgressSpeed.secondary">{{ detailProgressSpeed.secondary }}</small></strong></p>
                <p><span>平均速度</span><strong class="attachment-details-rate"><span>{{ detailProgressAverageSpeed.primary }}</span><small v-if="detailProgressAverageSpeed.secondary">{{ detailProgressAverageSpeed.secondary }}</small></strong></p>
                <p><span>峰值速度</span><strong class="attachment-details-rate"><span>{{ detailProgressPeakSpeed.primary }}</span><small v-if="detailProgressPeakSpeed.secondary">{{ detailProgressPeakSpeed.secondary }}</small></strong></p>
                <p><span>预计剩余</span><strong>{{ detailProgressEta }}</strong></p>
                <p><span>已耗时</span><strong>{{ detailProgressElapsed }}</strong></p>
              </div>
            </div>
            <div v-if="detailProgress" class="attachment-details-section">
              <h4>网络调优</h4>
              <div class="attachment-details-grid">
                <p><span>分块 / 窗口</span><strong>{{ detailProgress.chunkSize ? formatBytes(detailProgress.chunkSize) : '兼容模式' }} · {{ detailProgress.windowSize ? `${detailProgress.windowSize} 块` : '逐块确认' }}</strong></p>
                <p><span>窗口数据量</span><strong>{{ detailProgress.windowBytes ? formatBytes(detailProgress.windowBytes) : '未提供' }}</strong></p>
                <p><span>在途数据</span><strong>{{ detailProgress.inFlightBytes ? formatBytes(detailProgress.inFlightBytes) : '未提供' }}</strong></p>
                <p><span>累计确认速度</span><strong>{{ detailProgress.confirmedThroughput ? `${formatSpeed(detailProgress.confirmedThroughput)}/S` : '正在测量' }}</strong></p>
                <p><span>确认批量</span><strong>{{ detailProgress.ackTargetBytes ? formatBytes(detailProgress.ackTargetBytes) : '逐窗口确认' }}</strong></p>
                <p><span>确认延迟</span><strong>{{ detailProgress.ackLatencyMs ? `${detailProgress.ackLatencyMs} ms` : '正在测量' }}</strong></p>
                <p><span>调优状态</span><strong>{{ tuningStateLabel(detailProgress.tuningState) }}</strong></p>
                <p><span>通道 / 模式</span><strong>{{ detailProgress.transport || 'TLS/TCP' }} · {{ transferModeLabel(detailProgress.transferMode) }}</strong></p>
                <p v-if="detailProgress.streamCount"><span>并行数据流</span><strong>{{ detailProgress.activeStreams || detailProgress.streamCount }} / {{ detailProgress.streamCount }} 路</strong></p>
              </div>
              <p v-if="detailProgress.tuningReason" class="attachment-details-reason" :title="detailProgress.tuningReason">{{ detailProgress.tuningReason }}</p>
            </div>
            <div class="attachment-details-section attachment-details-file-section">
              <h4>文件与校验</h4>
              <div class="attachment-details-grid">
                <p><span>大小 / 类型</span><strong>{{ formatBytes(attachmentDetails.fileSize) }} · {{ attachmentDetails.mimeType || '未知类型' }}</strong></p>
                <p><span>校验状态</span><strong>{{ detailProgress?.verified === true ? 'SHA-256 校验通过' : detailProgress?.verified === false ? '校验失败' : '等待最终校验' }}</strong></p>
                <p class="attachment-details-wide"><span>本地路径</span><strong class="attachment-details-ellipsis" :title="attachmentDetails.localPath || ''">{{ attachmentDetails.localPath || '尚未生成' }}</strong></p>
                <p class="attachment-details-wide"><span>SHA-256</span><strong class="mono attachment-details-ellipsis" :title="attachmentDetails.sha256 || ''">{{ attachmentDetails.sha256 || '尚未生成' }}</strong></p>
              </div>
            </div>
          </div>
        </a-modal>
        <div class="horizontal-resizer" @pointerdown="startResize('composer', $event)" title="调整输入框高度" />
        <footer class="composer" :class="{ 'composer-disabled': !activePeerCanSend }" :style="{ height: `${composerTotalHeight}px` }">
          <div class="composer-tools"><button class="emoji-toggle" title="表情" :disabled="!activePeerCanSend" @mousedown.prevent.stop="emojiOpen = !emojiOpen" @keydown.enter.space.prevent="emojiOpen = !emojiOpen"><icon-face-smile-fill /></button><button title="附件" :disabled="!activePeerCanSend" @mousedown.prevent.stop="pickFile" @keydown.enter.space.prevent="pickFile"><icon-folder /></button><button title="打开好友共享盘" :disabled="!activePeerCanSend" @mousedown.prevent.stop="openFriendSharedDrive" @keydown.enter.space.prevent="openFriendSharedDrive"><icon-cloud /></button></div>
          <div class="emoji-panel" :class="{ 'is-open': emojiOpen }" :aria-hidden="!emojiOpen" @pointerdown.stop><button v-for="emoji in emojis" :key="emoji" @pointerdown.prevent.stop="selectEmoji(emoji)" @keydown.enter.prevent.stop="selectEmoji(emoji)">{{ emoji }}</button></div>
          <div v-if="pendingFiles.length || pendingImages.length" class="pending-files" :style="{ height: `${pendingComposerListHeight}px` }"><div v-for="(file, index) in pendingFiles" :key="file.id" class="pending-file"><icon-file /><span :title="file.path">{{ file.name }}</span><small>{{ formatBytes(file.size) }}</small><button type="button" title="移除文件" @click="pendingFiles.splice(index, 1)"><icon-close /></button></div><div v-for="(image, index) in pendingImages" :key="image" class="pending-image"><img :src="image" /><button @click="pendingImages.splice(index, 1)"><icon-close /></button></div></div>
          <div class="composer-editor">
            <textarea ref="composerInput" v-model="draft" :disabled="!activePeerCanSend" :placeholder="activePeerCanSend ? '输入消息，Enter 发送，Shift + Enter 换行' : '当前不是好友，请重新申请好友'" @focus="handleComposerFocus" @pointerdown="markActiveRead" @paste="handlePaste" @keydown.enter.exact.prevent.stop="sendMessage" />
          </div>
          <div class="composer-foot"><span>{{ activePeerCanSend ? '消息将通过局域网加密传输' : '当前不是好友，请重新申请好友' }}</span><a-button type="primary" :loading="sendingMessage" :disabled="sendingMessage || !activePeerCanSend || (!draft.trim() && !pendingImages.length && !pendingFiles.length)" @click="sendMessage">发送</a-button></div>
          <button v-if="newMessageCount" class="new-message-button" @click="scrollToBottom(false, 'animated')">{{ newMessageCount }} 条新消息</button>
        </footer>
      </main>
      <main v-else class="blank-state"><div class="brand-mark">✦</div><h2>飞秋Pro</h2><p>选择一位好友开始聊天</p><a-button type="primary" @click="openDiscover">发现局域网好友</a-button></main>
      <div v-if="peerMenu.visible && peerMenu.peer" class="peer-context-menu" :style="peerMenuStyle" @click.stop @pointerdown.stop>
        <button @click="markPeerUnread(peerMenu.peer)">标记未读</button>
        <button @click="togglePeerPinned(peerMenu.peer)">{{ conversationForPeer(peerMenu.peer.deviceId)?.pinned ? '取消置顶' : '置顶' }}</button>
        <button class="danger" @click="hidePeerAndClear(peerMenu.peer)">删除</button>
      </div>
      <div v-if="deleteConfirm.visible && deleteConfirm.kind === 'hide' && deleteConfirm.peer" class="delete-confirm-popover" :style="deleteConfirmStyle" @click.stop @pointerdown.stop>
        <strong>隐藏“<span class="nickname-ellipsis-inline">{{ deleteConfirm.peer.remark || deleteConfirm.peer.nickname }}</span>”</strong>
        <p>仅隐藏本机好友列表并清除本机聊天记录，不会删除好友关系。对方再次发送文字消息时，会重新显示在列表中。</p>
        <div class="delete-confirm-actions"><button @click="closeDeleteConfirm">取消</button><button class="danger" @click="confirmPendingDelete">确认删除</button></div>
      </div>
    </section>

    <section v-if="mountedSections.favorites" v-show="section === 'favorites'" class="workspace favorites-workspace">
      <FavoritesPage :peers="store.peers" :active="section === 'favorites'" :preload="mountedSections.favorites" @forward="openFavoriteForward" />
    </section>

    <a-modal v-model:visible="forwardVisible" title="选择转发好友" @ok="confirmForward" @cancel="forwardVisible = false"><div class="forward-targets"><a-checkbox v-for="peer in forwardCandidates" :key="peer.deviceId" :model-value="forwardTargetIds.includes(peer.deviceId)" @change="toggleForwardTarget(peer.deviceId)"><span class="nickname-ellipsis">{{ peer.remark || peer.nickname }}</span></a-checkbox></div></a-modal>

    <section v-if="mountedSections.shared" v-show="section === 'shared'" class="workspace shared-embedded-workspace">
      <SharedDrivePage
        :key="`${sharedEmbeddedMode}:${sharedEmbeddedDeviceId}`"
        embedded-mode
        :owner-mode="sharedEmbeddedMode === 'owner'"
        :friend-device-id="sharedEmbeddedDeviceId"
        :active="section === 'shared'"
        :preload="mountedSections.shared"
      />
    </section>

    <section v-if="mountedSections.discover" v-show="section === 'discover'" class="workspace">
      <aside class="list-pane discovery-pane" :style="{ width: `${discoveryWidth}px`, flexBasis: `${discoveryWidth}px` }" @click.self="clearDiscoverySelection">
        <div class="pane-title"><a-button class="scan-button" size="small" :loading="scanning" :disabled="scanning" aria-label="重新扫描局域网设备" @click="refreshPeers">重新扫描</a-button></div>
        <div class="discovery-scroll" @scroll="closeAllContextMenus">
          <div class="discover-group" @contextmenu.prevent><div class="group-title-row"><button class="group-title" @click="groups.requests = !groups.requests"><span><icon-down v-if="groups.requests" /><icon-right v-else />新的朋友</span><b v-if="store.pendingRequests.length">{{ store.pendingRequests.length }}</b></button><button v-if="store.requests.length" class="clear-requests" @click.stop="clearRequestHistory">清除历史</button></div><button v-for="request in store.visibleRequests" v-show="groups.requests" :key="request.requestId" class="request-row" :class="{ selected: selectedRequest?.requestId === request.requestId, 'pending-request': isIncomingPending(request) }" @click="selectRequest(request)"><div class="avatar request-avatar" :style="avatarStyle(request.nickname)">{{ initials(request.nickname) }}<i v-if="isIncomingPending(request)" class="request-pending-dot" /></div><div class="request-copy"><strong class="nickname-ellipsis">{{ request.nickname || request.deviceId }}</strong><span>{{ requestDeviceLabel(request) }}</span><span class="request-status-line"><span class="request-status-text">{{ requestStatusText(request.status, request.direction, request.deviceId) }} · {{ request.message || (request.direction === 'mutual' ? '双方都发起了好友申请' : request.direction === 'sent' ? '我发起的好友申请' : '请求添加你为好友') }}</span><em v-if="isIncomingPending(request)" class="pending-request-mark">待处理</em></span></div></button></div>
      <div class="discover-group" @contextmenu.prevent><button class="group-title" @click="groups.discovered = !groups.discovered"><span><icon-down v-if="groups.discovered" /><icon-right v-else />已发现</span><b>{{ store.discovered.length }}</b></button><button v-for="peer in store.discovered" v-show="groups.discovered" :key="peer.deviceId" class="request-row" :class="{ selected: selectedDiscovery?.deviceId === peer.deviceId }" @click="selectDiscovery(peer)"><div class="avatar" :style="avatarStyle(peer.nickname, peer.avatarData)">{{ peer.avatarData ? '' : initials(peer.nickname) }}<i :class="{ online: peer.online }" /></div><div><strong class="nickname-ellipsis">{{ peer.nickname }}</strong><span>{{ peer.relation === 'friend' ? '已添加 · ' : '' }}{{ peer.platform }} · {{ peer.online ? '在线' : '离线' }}</span></div></button></div>
          <div class="discover-group" @contextmenu.prevent="closeContactMenu"><button class="group-title" @click="groups.contacts = !groups.contacts"><span><icon-down v-if="groups.contacts" /><icon-right v-else />通讯录</span><b>{{ store.contacts.length }}</b></button><button v-for="peer in store.contacts" v-show="groups.contacts" :key="`contact-${peer.deviceId}`" class="request-row" :class="{ selected: selectedDiscovery?.deviceId === peer.deviceId }" @click="selectContact(peer)" @contextmenu.prevent.stop="openContactMenu($event, peer)"><div class="avatar" :style="avatarStyle(peer.nickname, peer.avatarData)">{{ peer.avatarData ? '' : initials(peer.nickname) }}<i :class="{ online: peer.online }" /></div><div><strong class="nickname-ellipsis">{{ peer.remark || peer.nickname }}</strong><span>{{ peer.platform }} · {{ peer.online ? '在线' : '离线' }}</span></div></button></div>
        </div>
      </aside>
      <div class="vertical-resizer" @pointerdown="startResize('discover', $event)" title="调整列表宽度" />
        <main class="detail-pane" v-if="selectedRequest" @click="clearDiscoverySelection">
        <div class="detail-card" @click.stop><div class="avatar huge" :style="avatarStyle(selectedRequest.nickname)">{{ initials(selectedRequest.nickname) }}</div><h2 class="nickname-ellipsis">{{ selectedRequest.nickname }}</h2><div class="tags"><a-tag>{{ requestDeviceLabel(selectedRequest) }}</a-tag><a-tag :color="requestIsAccepted(selectedRequest) ? 'green' : selectedRequest.status === 'rejected' ? 'red' : 'arcoblue'">{{ requestStatusText(selectedRequest.status, selectedRequest.direction, selectedRequest.deviceId) }}</a-tag></div><p>{{ selectedRequest.message || (selectedRequest.direction === 'mutual' ? '双方都发起了好友申请' : '想和你成为好友') }}</p><div class="request-times"><span>申请时间<strong>{{ formatTime(selectedRequest.createdAt) }}</strong></span><span v-if="selectedRequest.acceptedAt">同意时间<strong>{{ formatTime(selectedRequest.acceptedAt) }}</strong></span></div><a-button v-if="requestIsAccepted(selectedRequest) && isFriend(selectedRequest.deviceId)" class="detail-primary-button" type="primary" long @click="openFriendChat(selectedRequest.deviceId)">打开聊天</a-button><div v-else-if="isIncomingPending(selectedRequest)" class="detail-actions"><a-button type="primary" :loading="processingRequests[selectedRequest.requestId]" @click="acceptRequest">同意</a-button><a-button status="danger" :disabled="processingRequests[selectedRequest.requestId]" @click="rejectRequest">拒绝</a-button></div></div>
      </main>
      <main class="detail-pane" v-else-if="selectedDiscovery" @click="clearDiscoverySelection">
        <div class="detail-card" @click.stop><div class="avatar huge" :style="avatarStyle(selectedDiscovery.nickname, selectedDiscovery.avatarData)">{{ selectedDiscovery.avatarData ? '' : initials(selectedDiscovery.nickname) }}</div><h2 class="nickname-ellipsis">{{ selectedDiscovery.nickname }}</h2><div class="tags"><a-tag>{{ selectedDiscovery.platform }}</a-tag><a-tag :color="selectedDiscovery.relation === 'friend' ? 'green' : 'arcoblue'">{{ selectedDiscovery.relation === 'friend' ? '已添加' : (selectedDiscovery.online ? '在线' : '最近可见') }}</a-tag></div><div class="basic-info"><label>设备类型<strong>{{ selectedDiscovery.platform }}</strong></label><label>操作系统<strong>{{ selectedDiscovery.osVersion }}</strong></label><label>状态<strong>{{ selectedDiscovery.online ? '在线' : '最近可见' }}</strong></label></div><a-button v-if="isFriend(selectedDiscovery.deviceId)" class="detail-primary-button" type="primary" long @click="openFriendChat(selectedDiscovery.deviceId)">打开聊天</a-button><a-button v-else class="detail-primary-button" type="primary" long @click="addPeer">发送好友申请</a-button><p class="subtle">{{ selectedDiscovery.relation === 'friend' ? '已添加，无需重复发送申请。' : '成为好友后，才会显示 IP、端口和完整设备指纹。' }}</p></div>
      </main>
      <main v-else class="blank-state"><div class="brand-mark">⌕</div><h2>发现局域网好友</h2><p>已开启“允许被发现”的设备会显示在这里</p><a-button type="primary" :loading="scanning" :disabled="scanning" @click="refreshPeers">立即扫描</a-button></main>
      <div v-if="contactMenu.visible && contactMenu.peer" class="contact-context-menu" :style="contactMenuStyle" @click.stop @pointerdown.stop>
        <button @click="chatFromContact(contactMenu.peer)">聊天</button>
        <button class="danger" @click="requestRemoveContact(contactMenu.peer)">删除</button>
      </div>
      <div v-if="deleteConfirm.visible && deleteConfirm.kind === 'remove' && deleteConfirm.peer" class="delete-confirm-popover" :style="deleteConfirmStyle" @click.stop @pointerdown.stop>
        <strong>删除“<span class="nickname-ellipsis-inline">{{ deleteConfirm.peer.remark || deleteConfirm.peer.nickname }}</span>”</strong>
        <p>将解除好友关系，但保留好友列表、聊天记录和本地附件。解除后需要重新建立好友关系。确定删除吗？</p>
        <div class="delete-confirm-actions"><button @click="closeDeleteConfirm">取消</button><button class="danger" @click="confirmPendingDelete">确认删除</button></div>
      </div>
    </section>

    <section v-if="mountedSections.settings" v-show="section === 'settings'" class="settings-shell">
      <header class="settings-head"><div><h2>设置</h2><p>管理个人资料、网络和应用行为</p></div><div class="settings-tabs"><button :class="{ active: settingsTab === 'general' }" @click="settingsTab = 'general'">通用</button><button :class="{ active: settingsTab === 'network' }" @click="settingsTab = 'network'">网络</button><button :class="{ active: settingsTab === 'device' }" @click="settingsTab = 'device'">设备信息</button><button :class="{ active: settingsTab === 'about' }" @click="settingsTab = 'about'">关于</button></div></header>
      <div class="settings-panel">
        <main class="settings-content" v-if="settingsTab === 'general'"><section class="setting-card profile-card"><button class="avatar-upload" type="button" title="更换头像" @click="chooseAvatar"><div class="avatar huge" :style="avatarStyle(editProfile.nickname, editProfile.avatarData)">{{ editProfile.avatarData ? '' : initials(editProfile.nickname) }}</div><span class="avatar-camera"><icon-camera /></span></button><div class="profile-edit"><a-input v-model="editProfile.nickname" label="昵称" :maxlength="10" @blur="syncNickname" @keyup.enter.prevent="saveProfile" /><p>昵称最多 10 个字符。</p><p>没有自定义头像时，系统会根据设备 ID 生成稳定头像。</p><div class="profile-buttons"><a-button type="primary" @mousedown.prevent="saveProfile">保存</a-button></div></div></section><section class="setting-card"><h3>外观</h3><div class="setting-line"><div><strong>主题</strong><span>选择应用的颜色主题</span></div><a-select v-model="editProfile.theme" style="width: 170px" @change="saveTheme"><a-option value="light">亮色</a-option><a-option value="dark">暗色</a-option><a-option value="system">跟随系统</a-option></a-select></div></section><section class="setting-card"><h3>隐私与启动</h3><div class="setting-line"><div><strong>允许被发现</strong><span>关闭后，局域网设备无法在发现列表看到你</span></div><a-switch v-model="editProfile.discoverable" @change="saveProfile(false)" /></div><div class="setting-line"><div><strong>开机启动</strong><span>登录系统后自动启动 FlyQPro</span></div><a-switch v-model="editProfile.launchAtStartup" @change="toggleStartup" /></div><div class="setting-line"><div><strong>自动保存附件</strong><span>关闭后，收到图片和文件需要手动选择接收、另存或拒绝</span></div><a-switch v-model="editProfile.autoSave" @change="saveProfile(false)" /></div></section><section class="setting-card"><h3>文件</h3><div class="setting-line"><div><strong>保存路径</strong><span class="path">{{ editProfile.fileSavePath || '未设置' }}</span></div><div class="path-actions"><a-button @click="chooseDirectory" :disabled="store.attachmentMigration.active">选择目录</a-button><a-button @click="resetAttachmentPath" :disabled="store.attachmentMigration.active || isDefaultPath" title="恢复为 FlyQPro 默认附件目录">重置</a-button></div></div></section><section class="setting-card"><h3>共享盘</h3><div class="setting-line"><div><strong>共享盘多窗打开</strong><span>开启后使用独立窗口，关闭后嵌入当前应用窗口</span></div><a-switch v-model="editProfile.sharedDriveMultiWindow" @change="saveProfile(false)" /></div></section></main>
      <main class="settings-content" v-else-if="settingsTab === 'network'"><section class="setting-card network-card"><div class="network-summary"><div class="network-dot" :class="store.network.status" /><div><strong>{{ store.network.status === 'normal' ? '网络正常' : '网络需要检查' }}</strong><span>{{ store.network.localIps.join('、') || '尚未获取局域网地址' }}</span></div><a-button type="primary" @click="runDiagnostic">网络诊断</a-button></div><div class="diagnostic-list" v-if="diagnostic"><div v-for="item in diagnostic.items" :key="item.name" class="diagnostic-row"><span :class="['diagnostic-icon', item.status]">{{ item.status === 'ok' ? '✓' : '!' }}</span><div><strong>{{ item.name }}</strong><span>{{ item.detail }} · {{ item.status === 'ok' ? '正常' : item.advice }}</span></div></div></div></section><section class="setting-card"><h3>监听信息</h3><div class="setting-line"><div><strong>UDP 发现端口</strong><span>用于局域网设备发现</span></div><code>{{ store.network.discoveryPort }}</code></div><div class="setting-line"><div><strong>TCP 发现端口</strong><span>UDP 不可用时的设备发现</span></div><code>{{ store.network.discoveryPort }}</code></div><div class="setting-line"><div><strong>TCP/TLS 聊天端口</strong><span>用于好友连接和消息传输</span></div><code>{{ store.network.chatPort || '启动中' }}</code></div><div class="setting-line"><div><strong>设备状态</strong><span>{{ store.network.peerCount }} 台已发现，{{ store.network.onlineCount }} 台在线</span></div><a-button @click="refreshPeers">重新扫描</a-button></div></section></main>
        <main class="settings-content" v-else-if="settingsTab === 'device'"><section class="setting-card device-card"><div class="device-card-head"><div><span class="device-eyebrow">本机身份</span><h3>设备信息</h3><p>用于局域网发现与加密连接的本机凭据</p></div><span class="device-badge"><i />本机</span></div><div class="device-fields"><label><span class="device-field-label"><i class="device-field-icon">⌘</i>平台</span><strong>{{ deviceInfo?.platform || '未知' }}</strong></label><label><span class="device-field-label"><i class="device-field-icon">▣</i>操作系统</span><strong>{{ deviceInfo?.osVersion || '未知' }}</strong></label><label><span class="device-field-label"><i class="device-field-icon">◈</i>通讯协议</span><strong>{{ deviceInfo?.protocolName || 'dzhgo' }}/{{ deviceInfo?.protocolMajor || 2 }}.0</strong></label><label><span class="device-field-label"><i class="device-field-icon">ID</i>设备 ID</span><strong class="mono">{{ deviceInfo?.deviceId || '尚未生成' }}</strong></label><label><span class="device-field-label"><i class="device-field-icon">✓</i>证书指纹</span><strong class="mono">{{ deviceInfo?.certificateFingerprint || '尚未生成' }}</strong></label></div><div class="device-card-foot">设备身份信息仅保存在本机，用于验证局域网连接安全性。</div></section></main>
        <main class="settings-content" v-else><section class="setting-card about-card"><div class="brand-mark">✦</div><h2 class="about-chinese-name">飞秋Pro</h2><p class="about-english-name">FlyQPro</p><p>局域网点对点聊天工具</p><div class="about-rows"><span>应用版本<strong>{{ appVersion || '未知' }}</strong></span><span>协议版本<strong>{{ deviceInfo?.protocolName || 'dzhgo' }}/{{ deviceInfo?.protocolMajor || 2 }}.0</strong></span><span>技术栈<strong>Go · Wails v3 · Vue 3 · TypeScript · Arco Design · SQLite</strong></span><span>技术支持<strong>广州大智汇信息科技有限公司</strong></span><span>数据存储<strong>本地 SQLite</strong></span><div class="about-link-row"><span>开源仓库</span><button type="button" class="repo-link" title="在浏览器中打开开源仓库" @click="openRepository">github.com/gzdzh-cn/FlyQPro <icon-export /></button></div></div><a-button @click="termsVisible = true">使用条款与隐私说明</a-button></section></main>
      </div>
    </section>
    <a-modal v-model:visible="termsVisible" title="使用条款与隐私说明" hide-cancel><p>飞秋Pro 仅在局域网内进行点对点通信。聊天记录、设备信息和附件保存在本机，不上传云端。请确认你有权在当前网络中发现和联系其他设备。</p></a-modal>
    <div v-if="store.attachmentMigration.active || migrationResultVisible" class="migration-lock" @click.stop>
      <section class="migration-card" role="dialog" aria-modal="true" aria-label="附件迁移进度">
        <icon-loading v-if="store.attachmentMigration.active" class="migration-spinner" />
        <icon-check-circle v-else-if="!store.attachmentMigration.errorMessage" class="migration-success" />
        <icon-close-circle v-else class="migration-error" />
        <h3>{{ store.attachmentMigration.active ? '正在迁移附件' : store.attachmentMigration.errorMessage ? '附件迁移失败' : '附件迁移完成' }}</h3>
        <p class="migration-path">{{ store.attachmentMigration.targetRoot }}</p>
        <a-progress :percent="migrationPercent" :status="store.attachmentMigration.errorMessage ? 'danger' : 'normal'" />
        <p>{{ store.attachmentMigration.current }} / {{ store.attachmentMigration.total }} · 已迁移 {{ store.attachmentMigration.migrated }} · 跳过 {{ store.attachmentMigration.skipped }} · 失败 {{ store.attachmentMigration.failed }}</p>
        <p v-if="store.attachmentMigration.fileName" class="migration-file">{{ store.attachmentMigration.fileName }}</p>
        <p v-if="store.attachmentMigration.errorMessage" class="migration-error-text">{{ store.attachmentMigration.errorMessage }}</p>
        <a-button v-if="migrationResultVisible" type="primary" @click="migrationResultVisible = false">知道了</a-button>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Message, Modal } from '@arco-design/web-vue'
import { IconBookmark, IconCamera, IconCheckCircle, IconClose, IconCloseCircle, IconCloud, IconDown, IconFaceSmileFill, IconFile, IconFolder, IconLeft, IconLoading, IconMore, IconPlus, IconRight, IconSearch, IconSettings, IconUserGroup } from '@arco-design/web-vue/es/icon'
import { Browser, Clipboard, Events, System, Window } from '@wailsio/runtime'
import { AppBadgeService, ChatService, ImageViewerService, SharedDriveWindowService } from '/#/flyqpro/internal/service'
import { useChatStore } from '@/store/modules/chat'
import FavoritesPage from '../favorites/index.vue'
import SharedDrivePage from '../shared-drive/index.vue'
import type { AttachmentDetails, FriendRequest, Message as ChatMessage, Peer } from '@/store/modules/chat/types'

const store = useChatStore()
type AppSection = 'friends' | 'discover' | 'favorites' | 'shared' | 'settings'
const section = ref<AppSection>('friends')
const mountedSections = reactive<Record<AppSection, boolean>>({ friends: true, discover: false, favorites: false, shared: false, settings: false })
const sharedEmbeddedMode = ref<'owner' | 'friend'>('owner')
const sharedEmbeddedDeviceId = ref('')
const sharedReturnSection = ref<AppSection>('friends')
const sharedReturnPeerId = ref('')
const settingsTab = ref('general')
const friendSearch = ref('')
const draft = ref('')
const quoteMessageId = ref('')
const quoteContent = ref('')
const sendingMessage = ref(false)
const showPeerInfo = ref(false)
const selfAvatarPreviewVisible = ref(false)
const myQRCode = ref('')
const selectedRequest = ref<FriendRequest>()
const selectedDiscovery = ref<Peer>()
const processingRequests = reactive<Record<string, boolean>>({})
const termsVisible = ref(false)
const diagnostic = ref<any>()
const deviceInfo = ref<any>()
const appVersion = ref('')
const isDark = ref(false)
const isMac = ref(false)
const editProfile = reactive({ ...store.profile })
const groups = reactive({ requests: false, contacts: false, discovered: false })
function storedSize(key: string, fallback: number, min: number, max: number) {
	const legacyKey = key.replace('flyqpro.', 'popchat.')
	const stored = localStorage.getItem(key)
	const legacy = stored === null ? localStorage.getItem(legacyKey) : null
	if (stored === null && legacy !== null) localStorage.setItem(key, legacy)
	const value = Number(stored ?? legacy)
	return Number.isFinite(value) ? Math.min(max, Math.max(min, value)) : fallback
}
const friendsWidth = ref(storedSize('flyqpro.friendsWidth', 310, 220, 440))
const discoveryWidth = ref(storedSize('flyqpro.discoveryWidth', 320, 240, 460))
const composerHeight = ref(storedSize('flyqpro.composerHeight', 158, 120, 320))
const emojiOpen = ref(false)
const composerInput = ref<HTMLTextAreaElement>()
const emojis = [...new Set('😀 😃 😄 😁 😆 😅 😂 🤣 😊 😇 🙂 🙃 😉 😌 😍 🥰 😘 😗 😙 😚 😋 😛 😝 😜 🤪 🤨 🧐 🤓 😎 🤩 🥳 😏 😒 😞 😔 😟 😕 🙁 ☹️ 😣 😖 😫 😩 🥺 😢 😭 😤 😠 😡 🤬 🤯 😳 🥵 🥶 😱 😨 😰 😥 😓 🤗 🤔 🫡 🤭 🫢 🤫 🤥 😶 😐 😑 😬 🫠 🙄 😯 😦 😧 😮 😲 🥱 😴 🤤 😪 😵 🤐 🥴 🤢 🤮 🤧 😷 🤒 🤕 🤑 🤠 😈 👿 👹 👺 🤡 💩 👻 💀 ☠️ 👽 👾 🤖 🎃 😺 😸 😹 😻 😼 😽 🙀 😿 😾 👋 🤚 🖐️ ✋ 🖖 👌 🤏 ✌️ 🤞 🫰 🤟 🤘 🤙 👈 👉 👆 👇 ☝️ 👍 👎 ✊ 👊 🤛 🤜 👏 🙌 👐 🤲 🙏 ✍️ 💅 🤝 💪 🦾 🖕 👂 🦻 👃 🧠 🫀 🫁 🦷 🦴 👀 👁️ 👅 👄 ❤️ 🧡 💛 💚 💙 💜 🖤 🤍 🤎 💔 ❣️ 💕 💞 💓 💗 💖 💘 💝 💟 ☮️ ✝️ ☪️ 🕉️ ☸️ ✡️ 🔯 🕎 ☯️ ☦️ 🛐 ⛎ ♈ ♉ ♊ ♋ ♌ ♍ ♎ ♏ ♐ ♑ ♒ ♓ 🆔 ⚛️ 🉑 ☢️ ☣️ 📛 🚫 ⛔ 📵 🚯 🚳 🚷 🔞 📶 🚸 ⚠️ 🔱 ♻️ ✅ ❇️ ✳️ ❎ 🌐 💠 Ⓜ️ 🌀 💤 🆚 🆗 🆕 🆓 🆒 🆘 🛑 ⛽ 🚧 🔰 ♻️ 💯 🔥 ✨ ⭐ 🌟 💫 💥 💢 💦 💨 🕳️ 💬 👁️‍🗨️ 🗨️ 🗯️ 💭 💤 🎉 🎊 🎈 🎁 🎀 🎂 🍰 🥂 🍻 ☕ 🍵 🧋 🍺 🍷 🥤 🍔 🍟 🍕 🌮 🍣 🍜 🍎 🍉 🍓 🥑 ⚽ 🏀 🏈 ⚾ 🎾 🏐 🏓 🥊 🏆 🥇 🎮 🎲 🎵 🎶 🎸 🎹 🎤 📷 📸 💻 🖥️ ⌚ 📱 💡 🔋 🔌 💰 💎 🚗 ✈️ 🚀 🛸 🏠 🏢 🌈 ☀️ 🌙 ⛅ ❄️ ☔ 🌊 🌍'.split(' '))]
const pendingImages = ref<string[]>([])
type PendingFile = { id: string; path: string; name: string; size: number; mime: string }
const pendingFiles = ref<PendingFile[]>([])
const pendingComposerListHeight = computed(() => {
	const itemCount = pendingFiles.value.length + pendingImages.value.length
	if (!itemCount) return 0
	const rows = Math.min(3, Math.max(1, Math.ceil(itemCount / 2)))
	const rowHeight = pendingImages.value.length ? 50 : 35
	return Math.min(84, rows * rowHeight + 1)
})
// The extra two pixels account for the additional flex gap introduced by the list.
const pendingComposerExtraHeight = computed(() => pendingComposerListHeight.value ? pendingComposerListHeight.value + 2 : 0)
const composerTotalHeight = computed(() => composerHeight.value + pendingComposerExtraHeight.value)
const messagePreviews = reactive<Record<string, string>>({})
const retryingMessages = reactive<Record<string, boolean>>({})
const attachmentActions = reactive<Record<string, boolean>>({})
const peerRemark = ref('')
const clearingConversation = ref(false)
const messageScroll = ref<HTMLElement>()
const newMessageCount = ref(0)
const userNearBottom = ref(true)
const migrationResultVisible = ref(false)
const scanning = ref(false)
const messageMenu = reactive<{ visible: boolean; x: number; y: number; message?: ChatMessage }>({ visible: false, x: 0, y: 0 })
const peerMenu = reactive<{ visible: boolean; x: number; y: number; peer?: Peer }>({ visible: false, x: 0, y: 0 })
const contactMenu = reactive<{ visible: boolean; x: number; y: number; peer?: Peer }>({ visible: false, x: 0, y: 0 })
const deleteConfirm = reactive<{ visible: boolean; kind: 'hide' | 'remove'; x: number; y: number; peer?: Peer }>({ visible: false, kind: 'hide', x: 0, y: 0 })
const selectionMode = ref(false)
const selectedMessageIds = reactive(new Set<string>())
const attachmentDetailsVisible = ref(false)
const attachmentDetails = ref<AttachmentDetails>()
const attachmentDetailsMessage = ref<any>()
const detailAttachmentId = computed(() => attachmentDetailsMessage.value?.attachmentId || attachmentDetails.value?.attachmentId || '')
const detailProgress = computed<any>(() => transferProgressFor(attachmentDetailsMessage.value))
const detailPeer = computed<Peer | undefined>(() => {
  const conversationId = attachmentDetailsMessage.value?.conversationId || ''
  const peerId = conversationId.startsWith('conv-') ? conversationId.slice(5) : detailProgress.value?.peerDeviceId
  return store.peers.find((peer) => peer.deviceId === peerId) || activePeer.value
})
const forwardVisible = ref(false)
const forwardCandidates = ref<Peer[]>([])
const forwardSources = ref<ChatMessage[]>([])
const forwardTargetIds = ref<string[]>([])
let resizeState: { kind: 'friends' | 'discover' | 'composer'; startX: number; startY: number; startValue: number } | undefined
let notificationAudio: AudioContext | undefined
let audioUnlocked = false
let pendingNotificationTone = false
let cancelNativeDrop: (() => void) | undefined
let handleBrowserDrop: ((event: Event) => void) | undefined
const desktopForeground = ref(true)
const knownRequestStates = new Map<string, string>()
let requestWatchReady = false
let suppressScrollReadUntil = 0
let scrollScheduleFrame = 0
let scrollAnimationFrame = 0
let scrollAnimationToken = 0
let bottomSettleToken = 0
let menuWarmupIdleId: number | undefined
let menuWarmupTimer: number | undefined
let menuWarmupFrame = 0
let menuWarmupQueue: AppSection[] = []
let menuWarmupPaused = false

const activePeer = computed(() => store.activePeer)
const conversationVisible = computed(() => section.value === 'friends' && Boolean(activePeer.value))
const activePeerCanSend = computed(() => Boolean(activePeer.value && activePeer.value.relation === 'friend' && activePeer.value.friendshipState !== 'removed'))
const orderedFriends = computed(() => [...store.friends].sort((left, right) => {
  const leftConversation = conversationForPeer(left.deviceId)
  const rightConversation = conversationForPeer(right.deviceId)
  if (Boolean(leftConversation?.pinned) !== Boolean(rightConversation?.pinned)) return leftConversation?.pinned ? -1 : 1
  const leftHasTime = Boolean(leftConversation?.lastMessageAt)
  const rightHasTime = Boolean(rightConversation?.lastMessageAt)
  if (leftHasTime !== rightHasTime) return leftHasTime ? -1 : 1
  if (leftHasTime && rightHasTime) {
    const timeDifference = new Date(rightConversation?.lastMessageAt || '').getTime() - new Date(leftConversation?.lastMessageAt || '').getTime()
    if (Number.isFinite(timeDifference) && timeDifference !== 0) return timeDifference
  }
  const leftName = (left.remark || left.nickname || '').toLocaleLowerCase()
  const rightName = (right.remark || right.nickname || '').toLocaleLowerCase()
  return leftName.localeCompare(rightName, 'zh-Hans') || left.deviceId.localeCompare(right.deviceId)
}))
const filteredFriends = computed(() => {
  const keyword = friendSearch.value.trim().toLowerCase()
  if (!keyword) return orderedFriends.value
  return orderedFriends.value.filter((peer) => `${peer.remark || ''} ${peer.nickname}`.toLowerCase().includes(keyword))
})
const totalUnreadCount = computed(() => {
  // Only conversations that still have a visible row contribute to the
  // Friends badge.  A hide/clear operation can briefly race with a queued
  // peer or message event; counting every conversation would leave a badge
  // behind even though the corresponding friend has disappeared.
  const visiblePeerIds = new Set(store.friends.map((peer) => peer.deviceId))
  return store.conversations.reduce((total, conversation) => (
    visiblePeerIds.has(conversation.peerDeviceId)
      ? total + Math.max(0, conversation.unreadCount || 0)
      : total
  ), 0)
})
const appBadgeCount = computed(() => totalUnreadCount.value + store.pendingRequests.length)
const activeMessages = computed(() => activePeer.value ? store.messages[`conv-${activePeer.value.deviceId}`] || [] : [])
const activeMessageLoadKey = computed(() => activeMessages.value.map((message) => `${message.messageId}:${message.kind}:${message.attachmentId || ''}:${message.attachmentStatus || ''}:${message.attachmentPath || ''}:${message.attachmentThumbnail ? 'thumbnail' : ''}`).join('|'))
const activeTransferLoadKey = computed(() => activeMessages.value.map((message) => { const progress = message.attachmentId ? (store.transferProgress[message.attachmentId] || store.transferHistory[message.attachmentId]) : undefined; return `${message.messageId}:${progress?.phase || ''}:${progress?.transferred || 0}` }).join('|'))
const migrationPercent = computed(() => store.attachmentMigration.total ? Math.min(100, Math.round(store.attachmentMigration.current / store.attachmentMigration.total * 100)) : 0)
const isDefaultPath = computed(() => !editProfile.fileSavePath || editProfile.fileSavePath === defaultAttachmentPath.value)
const defaultAttachmentPath = ref('')

function normalizeNickname(value: unknown) {
  return Array.from(String(value || '').trim()).slice(0, 10).join('')
}
function initials(value: string) { return (value || '?').trim().slice(0, 1).toUpperCase() }
function avatarStyle(value: string, image?: string) { if (image) return { backgroundImage: `url(${image})`, backgroundSize: 'cover', backgroundPosition: 'center' }; let hash = 0; for (const char of value || '?') hash = (hash * 31 + char.charCodeAt(0)) >>> 0; const hue = hash % 360; return { background: `linear-gradient(135deg, hsl(${hue} 80% 65%), hsl(${(hue + 42) % 360} 75% 45%))` } }
const feiqID = computed(() => {
  const device = String(deviceInfo.value?.deviceId || '')
  if (!device) return '未生成'
  let hash = 2166136261
  for (const char of device) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619) >>> 0
  return `FQ${hash.toString(36).toUpperCase().padStart(10, '0').slice(0, 10)}`
})
function isImageMessage(message: any) { if (message?.attachmentMime) return message.attachmentMime.startsWith('image/'); return /\.(avif|bmp|gif|jpe?g|png|webp)$/i.test(message?.attachmentName || message?.content || '') }
function formatTime(value: string) {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const now = new Date()
  const day = new Date(date.getFullYear(), date.getMonth(), date.getDate())
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const diff = Math.round((today.getTime() - day.getTime()) / 86400000)
  const time = `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
  const monday = new Date(today)
  monday.setDate(today.getDate() - ((today.getDay() + 6) % 7))
  const weekdays = ['日', '一', '二', '三', '四', '五', '六']
  if (diff === 0) return time
  if (diff === 1) return `昨天 ${time}`
  if (diff === 2) return `前天 ${time}`
  if (day >= monday && day < today) return `周${weekdays[date.getDay()]} ${time}`
  return `${date.getMonth() + 1}月${date.getDate()}日 ${time}`
}
function formatLastSeen(value: string) { return value ? formatTime(value) : '未知' }
function messageStatusText(status: string, kind = 'text', attachmentStatus = '', sentByMe = false) {
  if (kind === 'file') {
    const fileStatus = attachmentStatus || status
    if (fileStatus === 'sent' || fileStatus === 'delivered') return '发送成功'
    if (fileStatus === 'read') return ''
    if (fileStatus === 'rejected') return sentByMe ? '对方已拒绝' : '我已拒绝'
    if (fileStatus === 'pending') return sentByMe ? '发送中' : '等待接收'
    if (sentByMe && (fileStatus === 'sending' || fileStatus === 'receiving')) return '发送中'
    return ({ preparing_thumbnail: '图片处理中', sending: '发送中', receiving: '接收中', canceled: '已取消', not_friend: '不是好友', failed: '发送失败' } as Record<string, string>)[fileStatus] || ''
  }
  if (status === 'sent') return '已发送'
  return ({ sending: '发送中', delivered: '发送成功', read: '已读', queued: '发送失败', not_friend: '不是好友', failed: '发送失败' } as Record<string, string>)[status] || status
}
function conversationForPeer(deviceId: string) { return store.conversations.find((conversation) => conversation.peerDeviceId === deviceId) }
function unreadCount(deviceId: string) { return conversationForPeer(deviceId)?.unreadCount || 0 }
function unreadLabel(count: number) { return count > 99 ? '99+' : String(count) }
function requestIsAccepted(request: FriendRequest) { return request.status === 'accepted' || isFriend(request.deviceId) }
function requestStatusText(status: string, direction = '', deviceId = '') { if (status === 'accepted' || (deviceId && isFriend(deviceId))) return '已成为好友'; if (status === 'pending' && direction === 'mutual') return '双方已申请'; return ({ pending: '待处理', rejected: '已拒绝', sent: '等待对方处理', queued: '等待发送' } as Record<string, string>)[status] || '申请记录' }
function isIncomingPending(request: FriendRequest) { return request.status === 'pending' && request.direction !== 'sent' && !isFriend(request.deviceId) }
async function selectEmoji(emoji: string) {
  draft.value += emoji
  emojiOpen.value = false
  await nextTick()
  const input = composerInput.value
  if (!input || input.disabled) return
  input.focus()
  const cursor = input.value.length
  input.setSelectionRange(cursor, cursor)
}
function requestDeviceLabel(request: FriendRequest) { const peer = store.peers.find((item) => item.deviceId === request.deviceId); if (!peer) return '设备信息同步中'; return [peer.platform, peer.osVersion].filter(Boolean).join(' · ') || '未知设备' }
function peerDeviceLabel(peer: Peer) { return [peer.platform, peer.osVersion].filter(Boolean).join(' · ') || '未知设备' }
function applyTheme(theme: string) { const dark = theme === 'dark' || (theme === 'system' && window.matchMedia?.('(prefers-color-scheme: dark)').matches); isDark.value = Boolean(dark); const windowBackground = dark ? '#0f1115' : '#edf0f3'; document.documentElement.style.setProperty('--window-corner-bg', windowBackground); document.body.style.backgroundColor = windowBackground; if (dark) { document.body.setAttribute('arco-theme', 'dark'); document.body.classList.add('flyqpro-dark') } else { document.body.removeAttribute('arco-theme'); document.body.classList.remove('flyqpro-dark') } }
async function refreshMyQRCode() { try { myQRCode.value = await ChatService.GetMyQRCode() } catch { myQRCode.value = '' } }
async function load() { try { store.profile = await ChatService.GetProfile(); Object.assign(editProfile, store.profile); applyTheme(store.profile.theme); deviceInfo.value = await ChatService.GetDeviceInfo(); await refreshMyQRCode(); appVersion.value = await ChatService.GetAppVersion(); if (deviceInfo.value?.identityStatus === 'hardware_identity_unavailable') Message.warning('系统安全凭据不可用，当前设备已生成新的身份'); store.setDeviceId(deviceInfo.value?.deviceId || ''); store.peers = await ChatService.ListPeers(); store.requests = await ChatService.ListFriendRequests(); store.conversations = await ChatService.ListConversations(); store.network = await ChatService.NetworkStatus(); if (section.value === 'friends' && !activePeer.value && store.friends.length) void loadConversation(store.friends[0], false) } catch (error: any) { Message.error(error?.message || '初始化聊天服务失败') } }
type IdleWindow = Window & { requestIdleCallback?: (callback: () => void, options?: { timeout: number }) => number; cancelIdleCallback?: (id: number) => void }
function clearMenuWarmupTask() {
  const idleWindow = window as IdleWindow
  if (menuWarmupIdleId !== undefined) idleWindow.cancelIdleCallback?.(menuWarmupIdleId)
  if (menuWarmupTimer !== undefined) window.clearTimeout(menuWarmupTimer)
  if (menuWarmupFrame) cancelAnimationFrame(menuWarmupFrame)
  menuWarmupIdleId = undefined
  menuWarmupTimer = undefined
  menuWarmupFrame = 0
}
function pauseMenuWarmup() {
  if (menuWarmupPaused) return
  menuWarmupPaused = true
  menuWarmupQueue = []
  clearMenuWarmupTask()
}
function runNextMenuWarmup() {
  if (menuWarmupPaused || !menuWarmupQueue.length) return
  const next = menuWarmupQueue.shift()
  if (!next) return
  mountedSections[next] = true
  menuWarmupTimer = window.setTimeout(() => {
    menuWarmupTimer = undefined
    scheduleNextMenuWarmup()
  }, 0)
}
function scheduleNextMenuWarmup() {
  if (menuWarmupPaused || !menuWarmupQueue.length) return
  const idleWindow = window as IdleWindow
  const run = () => {
    menuWarmupIdleId = undefined
    if (menuWarmupPaused) return
    runNextMenuWarmup()
  }
  if (idleWindow.requestIdleCallback) menuWarmupIdleId = idleWindow.requestIdleCallback(run, { timeout: 800 })
  else menuWarmupTimer = window.setTimeout(run, 100)
}
function scheduleMenuWarmup() {
  if (menuWarmupPaused) return
  clearMenuWarmupTask()
  const sectionsToWarm: AppSection[] = ['discover', 'settings', 'favorites', 'shared']
  menuWarmupQueue = sectionsToWarm.filter((item) => !mountedSections[item])
  menuWarmupFrame = requestAnimationFrame(() => {
    menuWarmupFrame = 0
    scheduleNextMenuWarmup()
  })
}
function chatScrollKey(deviceId: string) { return `flyqpro.chatScroll.v2.${deviceId}` }
function legacyChatScrollKey(deviceId: string) { return `popchat.chatScroll.${deviceId}` }
function saveActiveScrollPosition() {
  bottomSettleToken++
  const peer = activePeer.value
  const el = messageScroll.value
  if (!peer || !el) return
  const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80
  if (nearBottom) localStorage.removeItem(chatScrollKey(peer.deviceId))
  else localStorage.setItem(chatScrollKey(peer.deviceId), String(Math.max(0, el.scrollTop)))
}
async function restoreChatScrollPosition(deviceId: string) {
  await nextTick()
  for (let frame = 0; frame < 2; frame++) await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
  const el = messageScroll.value
  if (!el || activePeer.value?.deviceId !== deviceId) return
  let savedValue = localStorage.getItem(chatScrollKey(deviceId))
  if (savedValue === null) {
    const legacyKey = legacyChatScrollKey(deviceId)
    const legacyValue = localStorage.getItem(legacyKey)
    if (legacyValue !== null && Number(legacyValue) > 0) {
      savedValue = legacyValue
      localStorage.setItem(chatScrollKey(deviceId), legacyValue)
    }
    if (legacyValue !== null) localStorage.removeItem(legacyKey)
  }
  const saved = Number(savedValue)
  if (savedValue !== null && Number.isFinite(saved) && saved >= 0) {
    const maxScrollTop = Math.max(0, el.scrollHeight - el.clientHeight)
    el.scrollTop = Math.min(saved, maxScrollTop)
    userNearBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 80
    if (userNearBottom.value) localStorage.removeItem(chatScrollKey(deviceId))
    return
  }
  // No saved position means that the latest message must be visible. Set it
  // immediately, then keep correcting while rows, fonts and image previews
  // finish changing the scroll height.
  userNearBottom.value = true
  el.scrollTop = el.scrollHeight
  void settleChatBottom(deviceId)
}

function clearCurrentConversation() {
  const peer = activePeer.value
  if (!peer || clearingConversation.value) return
  Modal.confirm({
    title: '清除聊天记录',
    content: `确定清除与“${peer.remark || peer.nickname}”的全部本地聊天记录、图片和文件吗？此操作不可恢复，但不会删除好友关系。`,
    okText: '清除记录',
    cancelText: '取消',
    okButtonProps: { status: 'danger' },
    onOk: async () => {
      if (clearingConversation.value) return
      clearingConversation.value = true
      const peerDeviceId = peer.deviceId
      try {
        const result = await ChatService.ClearConversation(peerDeviceId)
        const removedMessages = store.clearConversationLocal(peerDeviceId)
        removedMessages.forEach((message: any) => {
          delete messagePreviews[message.messageId]
          if (message.attachmentId) { delete store.transferProgress[message.attachmentId]; delete store.transferHistory[message.attachmentId]; delete store.transferProgressByDirection[message.attachmentId]; delete store.transferHistoryByDirection[message.attachmentId] }
        })
        newMessageCount.value = 0
        userNearBottom.value = true
        localStorage.removeItem(chatScrollKey(peerDeviceId))
        showPeerInfo.value = false
        const skipped = result?.skippedExternalFiles ? `，保留 ${result.skippedExternalFiles} 个本机原始文件` : ''
        Message.success(`聊天记录已清除${skipped}`)
      } catch (error: any) {
        Message.error(error?.message || '清除聊天记录失败')
        throw error
      } finally {
        clearingConversation.value = false
      }
    },
  })
}
async function settleChatBottom(deviceId: string) {
  const token = ++bottomSettleToken
  const waitFrame = () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
  const correct = () => {
    const el = messageScroll.value
    if (!el || activePeer.value?.deviceId !== deviceId || !userNearBottom.value) return false
    el.scrollTop = el.scrollHeight
    return true
  }
  for (let frame = 0; frame < 12; frame++) {
    await waitFrame()
    if (token !== bottomSettleToken || !correct()) return
  }
  // Attachment previews can arrive after the initial Vue layout. These two
  // bounded checkpoints close the small gap caused by that late reflow
  // without keeping a permanent observer or taking control from the user.
  for (const delay of [100, 220]) {
    await new Promise<void>((resolve) => window.setTimeout(resolve, delay))
    if (token !== bottomSettleToken || !correct()) return
  }
}
async function loadConversation(peer: Peer, markRead: boolean, preserveViewport = false, forceLatest = false) {
  const previousPeerId = activePeer.value?.deviceId
  const cachedMessages = Boolean(store.messages[`conv-${peer.deviceId}`])
  saveActiveScrollPosition()
  store.selectPeer(peer.deviceId)
  void refreshPeerAvatar(peer.deviceId)
  showPeerInfo.value = false
  peerRemark.value = peer.remark || ''
  newMessageCount.value = 0
  if (markRead) store.clearConversationUnread(peer.deviceId)
  try {
    const id = await ChatService.EnsureConversation(peer.deviceId)
    const messages = await ChatService.ListMessages(id)
    store.messages[id] = messages
    if (forceLatest) localStorage.removeItem(chatScrollKey(peer.deviceId))
    const shouldRestore = forceLatest || !(preserveViewport && previousPeerId === peer.deviceId && cachedMessages)
    if (shouldRestore) await restoreChatScrollPosition(peer.deviceId)
    if (markRead) await ChatService.MarkConversationRead(peer.deviceId)
  } catch { /* the conversation can still be restored from the live store */ }
}
async function refreshPeerAvatar(deviceId: string) {
  try { await ChatService.RefreshPeerAvatar(deviceId) } catch { /* best effort; keep the cached avatar */ }
}
function selectPeer(peer: Peer) { closePeerMenu(); closeContactMenu(); closeDeleteConfirm(); void loadConversation(peer, true, false, true) }
function switchSection(target: AppSection) {
  if (store.attachmentMigration.active || section.value === target) return false
  const previous = section.value
  // Update the visible section synchronously. Vue renders the mounted page on
  // the next tick while its data requests continue in the background.
  mountedSections[target] = true
  section.value = target
  if (previous === 'friends') saveActiveScrollPosition()
  closeAllContextMenus()
  if (target !== 'friends') closePeerInfo()
  return true
}
function openDiscover() { switchSection('discover') }
function openFavorites() { switchSection('favorites') }
function enterFriends() {
  if (!switchSection('friends')) return
  const previousPeerId = activePeer.value?.deviceId
  const selected = activePeer.value && store.friends.some((peer) => peer.deviceId === activePeer.value?.deviceId) ? activePeer.value : store.friends[0]
  if (selected) {
    const hasCachedMessages = Boolean(store.messages[`conv-${selected.deviceId}`])
    if (previousPeerId !== selected.deviceId || !hasCachedMessages) void loadConversation(selected, false, previousPeerId === selected.deviceId)
  } else {
    // The current peer may have just been downgraded to a stranger after a
    // remote friendship rejection. Do not leave its conversation rendered
    // beside an empty friends list.
    store.selectPeer('')
    showPeerInfo.value = false
    newMessageCount.value = 0
    userNearBottom.value = true
  }
}
function openSettings(tab: string) { if (switchSection('settings')) settingsTab.value = tab }
async function saveProfile(showMessage = true) { editProfile.nickname = normalizeNickname(editProfile.nickname); try { const profile = await ChatService.UpdateProfile({ ...editProfile }); store.$patch({ profile: { ...store.profile, ...profile } }); Object.assign(editProfile, profile); applyTheme(profile.theme); await refreshMyQRCode(); if (showMessage) Message.success('设置已保存') } catch (error: any) { Message.error(error?.message || '保存失败') } }
async function saveTheme(theme: string) {
  const normalized = ["light", "dark", "system"].includes(theme) ? theme : "system"
  editProfile.theme = normalized
  applyTheme(normalized)
  try {
    const profile = await ChatService.SetTheme(normalized)
    store.$patch({ profile: { ...store.profile, ...profile } })
    Object.assign(editProfile, profile)
    applyTheme(profile.theme)
  } catch (error: any) {
    editProfile.theme = store.profile.theme || "system"
    applyTheme(editProfile.theme)
    Message.error(error?.message || "主题保存失败")
  }
}
function syncNickname() { editProfile.nickname = normalizeNickname(editProfile.nickname) }
async function toggleStartup() { try { store.profile = await ChatService.SetLaunchAtStartup(editProfile.launchAtStartup); Object.assign(editProfile, store.profile) } catch (error: any) { editProfile.launchAtStartup = !editProfile.launchAtStartup; Message.error(error?.message || '设置失败') } }
function confirmMigration(path: string) { return new Promise<boolean>((resolve) => { Modal.confirm({ title: '迁移附件保存路径', content: '现有附件将自动迁移到新路径。迁移期间应用无法操作其他页面，请勿退出应用。是否继续？', okText: '开始迁移', cancelText: '取消', onOk: () => resolve(true), onCancel: () => resolve(false) }) }) }
async function migrateAttachmentPath(path: string) {
  if (!path || path === editProfile.fileSavePath || store.attachmentMigration.active) return
  if (!(await confirmMigration(path))) return
  store.attachmentMigration = { ...store.attachmentMigration, active: true, phase: 'preparing', sourceRoot: editProfile.fileSavePath, targetRoot: path, current: 0, total: 0, migrated: 0, skipped: 0, failed: 0, unclassified: 0, errorMessage: '' }
  try {
    const result = await ChatService.MigrateAttachmentStorage(path)
    if (result.completed) {
      const profile = { ...store.profile, fileSavePath: path }
      store.profile = profile
      Object.assign(editProfile, profile)
      migrationResultVisible.value = true
    }
  } catch (error: any) {
    store.attachmentMigration = { ...store.attachmentMigration, active: false, phase: 'failed', errorMessage: error?.message || '附件迁移失败' }
    migrationResultVisible.value = true
    Message.error(error?.message || '附件迁移失败')
  }
}
async function chooseDirectory() { const path = await ChatService.PickDirectory(); if (path) await migrateAttachmentPath(path) }
async function resetAttachmentPath() { if (defaultAttachmentPath.value) await migrateAttachmentPath(defaultAttachmentPath.value) }
async function chooseAvatar() { const path = await ChatService.PickFile(); if (path) { try { store.profile = await ChatService.SetAvatar(path); Object.assign(editProfile, store.profile); await refreshMyQRCode(); Message.success('头像已更新') } catch (error: any) { Message.error(error?.message || '头像更新失败') } } }
async function resetAvatar() { try { const theme = editProfile.theme; const profile = await ChatService.ResetAvatar(); const nextProfile = { ...profile, theme: theme || profile.theme }; store.$patch({ profile: { ...store.profile, ...nextProfile } }); Object.assign(editProfile, nextProfile); applyTheme(theme || profile.theme); await refreshMyQRCode() } catch (error: any) { Message.error(error?.message || '恢复头像失败') } }
async function refreshPeers() { if (scanning.value) return; scanning.value = true; try { await ChatService.ScanPeers(); await new Promise((resolve) => setTimeout(resolve, 700)); store.peers = await ChatService.ListPeers(); store.network = await ChatService.NetworkStatus(); Message.success('已刷新局域网设备') } catch (error: any) { Message.error(error?.message || '扫描失败') } finally { scanning.value = false } }
function clearDiscoverySelection() { selectedRequest.value = undefined; selectedDiscovery.value = undefined; showPeerInfo.value = false; closeContactMenu(); closeDeleteConfirm() }
function selectRequest(request: FriendRequest) {
  if (selectedRequest.value?.requestId === request.requestId) {
    clearDiscoverySelection()
    return
  }
  selectedRequest.value = request
  selectedDiscovery.value = undefined
}
function clearRequestHistory() {
  Modal.confirm({
    title: '清除新的朋友',
    content: '将清除本机保存的全部好友申请历史，好友关系和通讯录不会受到影响。确定继续吗？',
    okText: '清除历史',
    cancelText: '取消',
    okButtonProps: { status: 'danger' },
    onOk: async () => {
      try {
        await ChatService.ClearFriendRequestHistory()
        store.requests = []
        selectedRequest.value = undefined
        Message.success('好友申请历史已清除')
      } catch (error: any) {
        Message.error(error?.message || '清除好友申请历史失败')
      }
    },
  })
}
function isFriend(deviceId: string): boolean { return store.contacts.some((peer) => peer.deviceId === deviceId && peer.relation === 'friend' && peer.friendshipState !== 'removed') }
async function openFriendChat(deviceId: string) {
  const peer = store.contacts.find((item) => item.deviceId === deviceId)
  if (!peer) {
    Message.warning('好友信息尚未同步，请稍后重试')
    return
  }
  closeContactMenu()
  closeDeleteConfirm()
  selectedRequest.value = undefined
  selectedDiscovery.value = undefined
  switchSection('friends')
  if (peer.friendshipState === 'removed') {
    Message.warning('该设备已不是好友，请先重新发送好友申请')
    return
  }
  if (peer.visibleInFriends === false) {
    try { await ChatService.RestoreHiddenFriend(deviceId); store.clearHiddenFriend(deviceId); store.peers = await ChatService.ListPeers() } catch (error: any) { Message.error(error?.message || '恢复好友显示失败'); return }
  }
  void loadConversation(peer, true, false, true)
}
function selectDiscovery(peer: Peer) {
  if (selectedDiscovery.value?.deviceId === peer.deviceId) {
    clearDiscoverySelection()
    return
  }
  selectedDiscovery.value = peer
  selectedRequest.value = undefined
}
function selectContact(peer: Peer) {
  selectedRequest.value = undefined
  selectedDiscovery.value = peer
}
async function addPeer() { if (!selectedDiscovery.value || isFriend(selectedDiscovery.value.deviceId)) return; try { const request = await ChatService.SendFriendRequest(selectedDiscovery.value.deviceId, '你好，我想和你成为好友'); store.requests = [request, ...store.requests.filter((item) => item.requestId !== request.requestId && item.deviceId !== request.deviceId)]; Message.success('好友申请已发送') } catch (error: any) { Message.error(error?.message || '发送申请失败') } }
async function processFriendRequest(request: FriendRequest, action: 'accept' | 'reject') {
  if (!isIncomingPending(request) || processingRequests[request.requestId]) return
  processingRequests[request.requestId] = true
  try {
    if (action === 'accept') {
      await ChatService.AcceptFriendRequest(request.requestId)
      store.clearHiddenFriend(request.deviceId)
      Message.success('已添加好友')
    } else {
      await ChatService.RejectFriendRequest(request.requestId)
      Message.success('已拒绝好友申请')
    }
    store.requests = await ChatService.ListFriendRequests()
    store.peers = await ChatService.ListPeers()
    if (selectedRequest.value?.requestId === request.requestId) selectedRequest.value = undefined
    selectedDiscovery.value = undefined
  } catch (error: any) {
    Message.error(error?.message || (action === 'accept' ? '同意申请失败' : '拒绝申请失败'))
  } finally {
    delete processingRequests[request.requestId]
  }
}
async function acceptRequest() { if (selectedRequest.value) await processFriendRequest(selectedRequest.value, 'accept') }
async function rejectRequest() { if (selectedRequest.value) await processFriendRequest(selectedRequest.value, 'reject') }
async function appendSentMessage(message: any) {
  const isNewActiveMessage = conversationVisible.value && message?.conversationId === `conv-${activePeer.value?.deviceId}` && !activeMessages.value.some((item) => item.messageId === message.messageId)
  store.handleEvent('chat:message', message)
  if (isNewActiveMessage) scheduleScrollToBottom(false, 'animated')
  await nextTick()
}
async function sendMessage() {
  // Enter can produce several key events before the asynchronous Wails call
  // returns. Claim the current draft synchronously so a rapid Enter or button
  // double-click cannot create multiple messages with new IDs. A later draft
  // typed while this send is in flight is kept for the next send.
  if (sendingMessage.value || !activePeerCanSend.value || !activePeer.value) return
  const peer = activePeer.value
  const content = draft.value.trim()
  const images = [...pendingImages.value]
  const files = [...pendingFiles.value]
  const quotedMessageId = quoteMessageId.value
  const quotedContent = quoteContent.value
  if (!content && !images.length && !files.length) return
  sendingMessage.value = true
  draft.value = ''
  quoteMessageId.value = ''
  quoteContent.value = ''
  pendingImages.value = []
  pendingFiles.value = []
  scrollToBottom()
  try {
    if (content) {
      const message = await ChatService.SendMessageWithMetadata(peer.deviceId, content, quotedMessageId, quotedContent, '')
      await appendSentMessage(message)
    }
    for (const image of images) {
      const message = await ChatService.SendImage(peer.deviceId, image)
      await appendSentMessage(message)
    }
    for (const file of files) {
      const message = await ChatService.SendFile(peer.deviceId, file.path)
      await appendSentMessage(message)
      notifyAttachmentResult(message)
    }
  } catch (error: any) {
    // Restore the consumed draft only when the user did not start composing a
    // new one while the original request was running.
    if (!draft.value && !pendingImages.value.length && !pendingFiles.value.length && activePeer.value?.deviceId === peer.deviceId) {
      draft.value = content
      quoteMessageId.value = quotedMessageId
      quoteContent.value = quotedContent
      pendingImages.value = images
      pendingFiles.value = files
    }
    Message.error(error?.message || '发送失败')
  } finally {
    sendingMessage.value = false
  }
}
function handlePaste(event: ClipboardEvent) { const files = Array.from(event.clipboardData?.files || []).filter((file) => file.type.startsWith('image/')); if (!files.length) return; event.preventDefault(); files.forEach((file) => { const reader = new FileReader(); reader.onload = () => { if (typeof reader.result === 'string') pendingImages.value.push(reader.result) }; reader.readAsDataURL(file) }) }
async function loadMessagePreview(message: any) {
  if (!message?.attachmentId || !isImageMessage(message)) return
  const progress = transferProgressFor(message)
  if (message.attachmentStatus === 'preparing_thumbnail' || progress?.phase === 'preparing_thumbnail') return
  const receiving = message.attachmentStatus === 'receiving' || Boolean(progress && progress.direction === 'receive' && progress.phase !== 'completed' && progress.phase !== 'failed')
  if (messagePreviews[message.messageId]) return
  try {
    const peerDeviceId = activePeer.value?.deviceId
    const shouldFollowBottom = Boolean(peerDeviceId && !localStorage.getItem(chatScrollKey(peerDeviceId)) && userNearBottom.value)
    try {
      messagePreviews[message.messageId] = await ChatService.GetAttachmentThumbnail(message.attachmentId)
    } catch {
      if (receiving) return
      messagePreviews[message.messageId] = await ChatService.GetAttachmentPreview(message.attachmentId)
    }
    if (shouldFollowBottom && activePeer.value?.deviceId === peerDeviceId) scheduleScrollToBottom(false, 'instant')
  } catch { /* pending remote image; clicking the image retries */ }
}
async function openImage(message: any) {
  closeMessageMenu()
  if (!message?.conversationId || !message?.messageId) {
    Message.warning('图片消息信息不完整')
    return
  }
  try {
    await ImageViewerService.OpenImageViewer(message.conversationId, message.messageId)
  } catch (error: any) {
    Message.error(error?.message || '打开图片查看器失败')
  }
}
function unlockNotificationAudio() {
  if (audioUnlocked) return
  try {
    const AudioContextClass = window.AudioContext || (window as any).webkitAudioContext
    if (!AudioContextClass) return
    notificationAudio = notificationAudio || new AudioContextClass()
    void notificationAudio.resume()
    audioUnlocked = true
    if (pendingNotificationTone) {
      pendingNotificationTone = false
      playNotificationTone()
    }
  } catch { /* browser audio may be unavailable */ }
}
function playNotificationTone() {
  if (!audioUnlocked || !notificationAudio) {
    pendingNotificationTone = true
    return
  }
  try {
    const context = notificationAudio
    const play = () => {
      const now = context.currentTime
      const master = context.createGain()
      master.gain.setValueAtTime(.72, now)
      master.connect(context.destination)
      const notes = [{ frequency: 880, start: 0, duration: .18 }, { frequency: 1175, start: .11, duration: .21 }]
      notes.forEach(({ frequency, start, duration }) => {
        const oscillator = context.createOscillator()
        const gain = context.createGain()
        oscillator.type = 'sine'
        oscillator.frequency.value = frequency
        const begins = now + start
        gain.gain.setValueAtTime(.0001, begins)
        gain.gain.exponentialRampToValueAtTime(.18, begins + .012)
        gain.gain.exponentialRampToValueAtTime(.0001, begins + duration)
        oscillator.connect(gain).connect(master)
        oscillator.start(begins)
        oscillator.stop(begins + duration + .01)
      })
    }
    if (context.state === 'suspended') void context.resume().then(play)
    else play()
  } catch { /* browser audio may be unavailable */ }
}
function updateDesktopForeground() {
  const visible = document.visibilityState === 'visible' && document.hasFocus()
  desktopForeground.value = visible
  if (visible) pendingNotificationTone = false
}
async function pickFile() {
  if (!activePeerCanSend.value || !activePeer.value) return
  const files = await ChatService.PickFiles()
  if (!files?.length) return
  addPendingPaths(files)
}
function pendingFileName(path: string) { return path.split(/[\\/]/).filter(Boolean).pop() || path }
function addPendingPaths(paths: string[], metadata: Partial<PendingFile> = {}) {
  paths.filter(Boolean).forEach((path) => {
    if (pendingFiles.value.some((file) => file.path === path)) return
    pendingFiles.value.push({ id: `${Date.now()}-${Math.random()}`, path, name: metadata.name || pendingFileName(path), size: metadata.size || 0, mime: metadata.mime || '' })
  })
}
async function retryMessage(message: any) {
  if (!message?.messageId || retryingMessages[message.messageId]) return
  retryingMessages[message.messageId] = true
  message.status = 'sending'
  if (message.kind === 'file') message.attachmentStatus = 'sending'
  try {
    const retried = message.kind === 'file'
      ? await ChatService.RetryAttachment(message.messageId)
      : await ChatService.RetryMessage(message.messageId)
    store.handleEvent('chat:message', retried)
    notifyAttachmentResult(retried)
  } catch (error: any) {
    message.status = 'failed'
    if (message.kind === 'file') message.attachmentStatus = 'failed'
    Message.error(error?.message || '附件重发失败')
  } finally {
    delete retryingMessages[message.messageId]
  }
}
async function acceptAttachment(message: any) {
  if (attachmentActionBusy(message)) return
  const previousStatus = message.attachmentStatus
  const previousMessageStatus = message.status
  attachmentActions[message.attachmentId] = true
  message.attachmentStatus = 'receiving'
  message.status = 'receiving'
  try {
    await nextTick()
    const attachment = await ChatService.AcceptAttachment(message.attachmentId)
    message.attachmentStatus = attachment.status
    message.attachmentPath = attachment.localPath
    await loadMessagePreview(message)
    Message.success('已开始接收文件')
  } catch (error: any) {
    message.attachmentStatus = previousStatus
    message.status = previousMessageStatus
    Message.error(error?.message || '接收文件失败')
  } finally { delete attachmentActions[message.attachmentId] }
}
async function saveAttachmentAs(message: any) {
  if (attachmentActionBusy(message)) return
  attachmentActions[message.attachmentId] = true
  try {
    await nextTick()
    const attachment = await ChatService.SaveAttachmentAs(message.attachmentId)
    if (attachment.status !== 'receiving' && attachment.status !== 'saved') return
    message.attachmentStatus = attachment.status
    message.attachmentPath = attachment.localPath
    await loadMessagePreview(message)
    Message.success('已开始接收文件')
  } catch (error: any) { Message.error(error?.message || '另存附件失败')
  } finally { delete attachmentActions[message.attachmentId] }
}
async function rejectAttachment(message: any) {
  if (attachmentActionBusy(message)) return
  const previousStatus = message.attachmentStatus
  const previousMessageStatus = message.status
  attachmentActions[message.attachmentId] = true
  message.attachmentStatus = 'rejected'
  message.status = 'rejected'
  try { await nextTick(); await ChatService.RejectAttachment(message.attachmentId); Message.info('已拒绝接收文件')
  } catch (error: any) { message.attachmentStatus = previousStatus; message.status = previousMessageStatus; Message.error(error?.message || '拒绝文件失败')
  } finally { delete attachmentActions[message.attachmentId] }
}
async function cancelAttachment(message: any) {
  if (attachmentActionBusy(message)) return
  const previousStatus = message.attachmentStatus
  const previousMessageStatus = message.status
  const isOutgoing = message.senderDeviceId === deviceInfo.value?.deviceId
  attachmentActions[message.attachmentId] = true
  message.attachmentStatus = 'canceled'
  message.status = 'canceled'
  try { await nextTick(); await ChatService.CancelAttachment(message.attachmentId); if (!isOutgoing) Message.info('文件传输已取消')
  } catch (error: any) { message.attachmentStatus = previousStatus; message.status = previousMessageStatus; Message.error(error?.message || '取消传输失败')
  } finally { delete attachmentActions[message.attachmentId] }
}
function attachmentActionBusy(message: any): boolean { return Boolean(message?.attachmentId && attachmentActions[message.attachmentId]) }
function notifyAttachmentResult(message: any) {
  switch (message?.attachmentStatus || message?.status) {
    case 'sent': Message.success('文件已发送'); break
    case 'rejected': Message.warning('对方已拒绝接收文件'); break
    case 'canceled': Message.info('文件传输已取消'); break
    case 'not_friend': Message.warning('不是好友'); break
    case 'failed': Message.error('文件发送失败'); break
    default: Message.info('文件正在等待对方接收')
  }
}
function attachmentNeedsDecision(message: any): boolean { return message?.senderDeviceId !== deviceInfo.value?.deviceId && message?.attachmentStatus === 'pending' }
function attachmentAwaitingAcceptance(message: any): boolean {
  if (message?.senderDeviceId !== deviceInfo.value?.deviceId || message?.attachmentStatus !== 'pending') return false
  const progress = transferProgressFor(message)
  return !progress || progress.phase === 'awaiting_acceptance'
}
function formatBytes(value: number) { if (!value) return '未知大小'; if (value < 1024) return `${value} B`; if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`; return `${(value / 1024 / 1024).toFixed(1)} MB` }
function formatSpeed(value: number) {
  const bytes = Math.max(0, Number(value || 0))
  if (!bytes) return '0 B'
  if (bytes >= 1024 * 1024 * 1024) return `${Math.round(bytes / 1024 / 1024 / 1024)} GB`
  if (bytes >= 1024 * 1024) return `${Math.round(bytes / 1024 / 1024)} MB`
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`
  return `${Math.round(bytes)} B`
}
function formatTransferRate(value?: number): { primary: string; secondary: string } {
  const bytes = Number(value || 0)
  if (!(bytes > 0)) return { primary: '正在测量', secondary: '' }
  const primary = `${formatSpeed(bytes)}/S`
  const bits = bytes * 8
  const secondary = bits >= 1000 * 1000 * 1000
    ? `${Math.round(bits / 1000 / 1000 / 1000)} Gbps`
    : `${Math.round(bits / 1000 / 1000)} Mbps`
  return { primary, secondary }
}
function formatDuration(value?: number) { const seconds = Math.max(0, Math.round(Number(value || 0) / 1000)); if (!seconds) return '正在测量'; const minutes = Math.floor(seconds / 60); return minutes ? `${minutes} 分 ${seconds % 60} 秒` : `${seconds} 秒` }
const detailProgressSpeed = computed(() => formatTransferRate(detailProgress.value?.speed))
const detailProgressAverageSpeed = computed(() => formatTransferRate(detailProgress.value?.averageSpeed))
const detailProgressPeakSpeed = computed(() => formatTransferRate(detailProgress.value?.peakSpeed))
const detailProgressEta = computed(() => detailProgress.value?.etaSeconds ? formatDuration(detailProgress.value.etaSeconds * 1000) : '暂不可估算')
const detailProgressElapsed = computed(() => formatDuration(detailProgress.value?.elapsedMs))
function transferPhaseLabel(phase?: string) { return ({ awaiting_acceptance: '等待对方接收', preparing_thumbnail: '文件准备中', transferring: '传输中', receiving: '接收中', 'remote-receive': '对方接收中', completed: '已完成', canceled: '已取消', rejected: '已拒绝', failed: '传输失败' } as Record<string, string>)[phase || ''] || phase || '未知' }
function transferDirectionLabel(direction?: string) { return ({ send: '发送', receive: '接收', 'remote-receive': '对方接收' } as Record<string, string>)[direction || ''] || direction || '未知' }
function tuningStateLabel(state?: string) { return ({ probing: '探测中', accelerating: '加速中', stable: '稳定', backing_off: '降速恢复' } as Record<string, string>)[state || ''] || state || '兼容模式' }
function transferModeLabel(mode?: string) { return ({ 'parallel-binary': '并行高速二进制', 'binary-window': '高速二进制', 'json-window': '兼容窗口', 'legacy-chunk': '逐块兼容' } as Record<string, string>)[mode || ''] || mode || '正在协商' }
const terminalTransferPhases = new Set(['completed', 'canceled', 'rejected', 'failed'])
function transferProgressFor(message: any): any {
  if (!message?.attachmentId) return undefined
  const attachmentId = message.attachmentId
  const directions = store.transferProgressByDirection[attachmentId] || store.transferHistoryByDirection[attachmentId]
  if (!directions) return store.transferProgress[attachmentId] || store.transferHistory[attachmentId]
  const mine = message.senderDeviceId === deviceInfo.value?.deviceId
  const preferred = mine ? directions['remote-receive'] : directions.receive
  const diagnostics = mine ? directions.send : directions.receive
  if (!preferred) {
    if (mine && diagnostics && ['binary-window', 'json-window'].includes(diagnostics.transferMode || '') && !terminalTransferPhases.has(diagnostics.phase)) {
      return { ...diagnostics, transferred: 0, remoteReceived: 0, speed: undefined, averageSpeed: undefined, peakSpeed: undefined, etaSeconds: undefined, elapsedMs: undefined }
    }
    return diagnostics || directions.send || directions.receive
  }
  const merged = { ...(diagnostics || {}), ...preferred }
  if (mine) {
    merged.sent = diagnostics?.sent ?? diagnostics?.transferred ?? merged.sent
    merged.remoteReceived = preferred.remoteReceived ?? preferred.transferred ?? 0
    merged.transferred = preferred.transferred ?? merged.remoteReceived
    merged.total = preferred.total || diagnostics?.total || message.attachmentSize || 0
  }
  const terminal = [diagnostics, preferred].find((item) => item && terminalTransferPhases.has(item.phase))
  if (terminal) {
    merged.phase = terminal.phase
    if (terminal.verified !== undefined) merged.verified = terminal.verified
  }
  return merged
}
function attachmentTransfer(details: any): any { return details?.attachmentId ? transferProgressFor(attachmentDetailsMessage.value) : undefined }
function transferProgressTransferred(message: any): number {
  const progress = transferProgressFor(message)
  if (!progress) return 0
  if (message.senderDeviceId === deviceInfo.value?.deviceId) return progress.remoteReceived ?? progress.sent ?? progress.transferred ?? 0
  return progress.received ?? progress.transferred ?? 0
}
function transferProgressPercent(message: any): number {
  const progress = transferProgressFor(message)
  if (!progress) return 0
  if (progress.phase === 'completed') return 100
  const total = progress.total || message.attachmentSize || 0
  return total ? Math.min(100, Math.round(transferProgressTransferred(message) / total * 100)) : (progress.percent || 0)
}
function transferSpeedLabel(message: any): string {
  const progress = transferProgressFor(message)
  return progress?.speed ? `${formatSpeed(progress.speed)}/S` : '正在测量'
}
function transferProgressLabel(message: any): string {
  const progress = transferProgressFor(message)
  if (!progress) return ''
  if (progress.phase === 'preparing_thumbnail') return '图片处理中'
  if (progress.phase === 'failed') return '传输失败'
  if (progress.phase === 'canceled') return '已取消'
  if (progress.phase === 'rejected') return '已拒绝'
  if (progress.phase === 'awaiting_acceptance') return '等待对方接收'
  if (progress.phase === 'completed') return message.senderDeviceId === deviceInfo.value?.deviceId ? '对方已接收' : '接收完成'
  if (message.senderDeviceId === deviceInfo.value?.deviceId) return '发送中'
  return '接收中'
}
function transferElapsedLabel(message: any): string {
  const progress = transferProgressFor(message)
  return progress?.elapsedMs ? formatDuration(progress.elapsedMs) : '正在测量'
}
function transferEtaLabel(message: any): string {
  const progress = transferProgressFor(message)
  return progress?.etaSeconds ? formatDuration(progress.etaSeconds * 1000) : '暂不可估算'
}
function imageTransferActive(message: any): boolean {
  const progress = transferProgressFor(message)
  return Boolean(progress && ['preparing_thumbnail', 'transferring', 'receiving', 'remote-receive'].includes(progress.phase))
}
function imageProgressRingStyle(message: any) {
  return { '--progress': `${transferProgressPercent(message)}%` }
}
const messageMenuStyle = computed(() => ({ left: `${messageMenu.x}px`, top: `${messageMenu.y}px` }))
const peerMenuStyle = computed(() => ({ left: `${peerMenu.x}px`, top: `${peerMenu.y}px` }))
const contactMenuStyle = computed(() => ({ left: `${contactMenu.x}px`, top: `${contactMenu.y}px` }))
const deleteConfirmStyle = computed(() => ({ left: `${deleteConfirm.x}px`, top: `${deleteConfirm.y}px` }))
function closeMessageMenu() { messageMenu.visible = false; messageMenu.message = undefined }
function closePeerMenu() { peerMenu.visible = false; peerMenu.peer = undefined }
function closeContactMenu() { contactMenu.visible = false; contactMenu.peer = undefined }
function closeDeleteConfirm() { deleteConfirm.visible = false; deleteConfirm.peer = undefined }
function closeAllContextMenus() {
  closeMessageMenu()
  closePeerMenu()
  closeContactMenu()
  closeDeleteConfirm()
}
function handleAppContextMenu(event: MouseEvent) {
  const target = event.target as Element | null
  // Keep the browser's native editing menu for editable controls. Every
  // other part of the app has either a business menu on its row/bubble or no
  // context actions at all.
  if (target?.closest('input, textarea, [contenteditable="true"]')) return
  event.preventDefault()
  if (target?.closest('.message-context-menu, .peer-context-menu, .contact-context-menu, .delete-confirm-popover')) return
  closeAllContextMenus()
}
function handleContextMenuKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') closeAllContextMenus()
}
function canOpenMessageMenu(message: ChatMessage): boolean {
  if (message.kind !== 'file') return true
  return attachmentHasLocalFile(message)
}
function openMessageMenu(event: MouseEvent, message: ChatMessage) {
  if (!canOpenMessageMenu(message)) {
    closeAllContextMenus()
    return
  }
  closePeerMenu()
  closeContactMenu()
  closeDeleteConfirm()
  closePeerInfo()
  messageMenu.message = message
  messageMenu.x = Math.max(8, Math.min(event.clientX, window.innerWidth - 198))
  messageMenu.y = Math.max(8, Math.min(event.clientY, window.innerHeight - 308))
  messageMenu.visible = true
}
function openPeerMenu(event: MouseEvent, peer: Peer) {
  closeMessageMenu()
  closeContactMenu()
  closeDeleteConfirm()
  peerMenu.peer = peer
  peerMenu.x = Math.min(event.clientX, Math.max(8, window.innerWidth - 180))
  peerMenu.y = Math.min(event.clientY, Math.max(8, window.innerHeight - 150))
  peerMenu.visible = true
}
function openContactMenu(event: MouseEvent, peer: Peer) {
  closeMessageMenu()
  closePeerMenu()
  closeDeleteConfirm()
  contactMenu.peer = peer
  contactMenu.x = Math.min(event.clientX, Math.max(8, window.innerWidth - 180))
  contactMenu.y = Math.min(event.clientY, Math.max(8, window.innerHeight - 110))
  contactMenu.visible = true
}
function requestHidePeerDelete(peer: Peer) {
  const x = peerMenu.x
  const y = peerMenu.y
  closePeerMenu()
  deleteConfirm.kind = 'hide'
  deleteConfirm.peer = peer
  deleteConfirm.x = Math.min(x, Math.max(8, window.innerWidth - 330))
  deleteConfirm.y = Math.min(y, Math.max(8, window.innerHeight - 190))
  deleteConfirm.visible = true
}
function requestRemoveContact(peer: Peer) {
  const x = contactMenu.x
  const y = contactMenu.y
  closeContactMenu()
  deleteConfirm.kind = 'remove'
  deleteConfirm.peer = peer
  deleteConfirm.x = Math.min(x, Math.max(8, window.innerWidth - 330))
  deleteConfirm.y = Math.min(y, Math.max(8, window.innerHeight - 190))
  deleteConfirm.visible = true
}
function chatFromContact(peer: Peer) {
  closeContactMenu()
  openFriendChat(peer.deviceId)
}
async function reloadPeerData() {
  store.peers = await ChatService.ListPeers()
  store.conversations = await ChatService.ListConversations()
}
async function markPeerUnread(peer: Peer) {
  closePeerMenu()
  try {
    await ChatService.MarkConversationUnread(peer.deviceId)
    const conversation = conversationForPeer(peer.deviceId)
    if (conversation) conversation.unreadCount = Math.max(1, conversation.unreadCount || 0)
    else store.conversations = await ChatService.ListConversations()
    Message.success('已标记为未读')
  } catch (error: any) {
    Message.error(error?.message || '标记未读失败')
  }
}
async function togglePeerPinned(peer: Peer) {
  const next = !conversationForPeer(peer.deviceId)?.pinned
  closePeerMenu()
  try {
    await ChatService.SetConversationPinned(peer.deviceId, next)
    await reloadPeerData()
    Message.success(next ? '已置顶' : '已取消置顶')
  } catch (error: any) {
    Message.error(error?.message || '更新置顶状态失败')
  }
}
function hidePeerAndClear(peer: Peer) { requestHidePeerDelete(peer) }
async function confirmPendingDelete() {
  const peer = deleteConfirm.peer
  const kind = deleteConfirm.kind
  closeDeleteConfirm()
  if (!peer) return
  try {
    if (kind === 'hide') await ChatService.HideFriendAndClearLocalData(peer.deviceId)
    else await ChatService.RemoveFriendAndClearLocalData(peer.deviceId)
    if (kind === 'hide') {
      for (const message of store.messages[`conv-${peer.deviceId}`] || []) {
        delete messagePreviews[message.messageId]
        if (message.attachmentId) { delete store.transferProgress[message.attachmentId]; delete store.transferHistory[message.attachmentId]; delete store.transferProgressByDirection[message.attachmentId]; delete store.transferHistoryByDirection[message.attachmentId] }
      }
      delete store.messages[`conv-${peer.deviceId}`]
      // Remove the local conversation snapshot as well.  The backend clears
      // it too, but removing it here closes the small race with stale
      // peer/message events and keeps the Friends badge in sync immediately.
      store.clearConversationLocal(peer.deviceId)
    }
    if (store.activePeerId === peer.deviceId && kind === 'hide') {
      store.selectPeer('')
      showPeerInfo.value = false
    }
    if (kind === 'hide') {
      store.hideFriendLocally(peer.deviceId)
      // Hide the row immediately.  The backend remains authoritative, but
      // an in-flight peer/status event must not leave the just-hidden row
      // visible until the subsequent refresh completes.
      store.peers = store.peers.map((item) => item.deviceId === peer.deviceId ? { ...item, visibleInFriends: false } : item)
    }
    await reloadPeerData()
    if (kind === 'hide') {
      // Keep the local list invariant even if a stale peer-updated event was
      // queued while the conversation records were being cleared.
      store.peers = store.peers.map((item) => item.deviceId === peer.deviceId ? { ...item, visibleInFriends: false } : item)
    }
    if (kind === 'remove') store.requests = await ChatService.ListFriendRequests()
    Message.success(kind === 'hide' ? '已隐藏好友并清除本机聊天记录' : '已解除好友关系，聊天记录已保留')
  } catch (error: any) {
    Message.error(error?.message || (kind === 'hide' ? '删除失败' : '删除好友失败'))
  }
}
function attachmentHasLocalFile(message: any) { return Boolean(message?.attachmentId && message?.attachmentPath && ['sent', 'saved'].includes(message?.attachmentStatus || message?.status)) }
function attachmentCompletedLocal(message: any) { return attachmentHasLocalFile(message) }
async function copyTextMessage(message: any) {
  closeMessageMenu()
  const content = String(message?.content || '')
  if (!content) { Message.warning('没有可复制的文字'); return }
  try {
    try { await Clipboard.SetText(content) } catch { await navigator.clipboard.writeText(content) }
    Message.success('已复制')
  } catch { Message.error('复制失败，请检查剪贴板权限') }
}
async function copyImageMessage(message: any) {
  closeMessageMenu()
  if (!attachmentHasLocalFile(message)) return
  try {
    const source = await ChatService.GetAttachmentImage(message.attachmentId)
    const response = await fetch(source)
    const blob = await response.blob()
    if (navigator.clipboard && 'ClipboardItem' in window) await navigator.clipboard.write([new ClipboardItem({ [blob.type]: blob })])
    else await navigator.clipboard.writeText(message.attachmentName || '图片')
    Message.success('已复制图片')
  } catch { Message.error('复制图片失败') }
}
async function saveAttachmentCopy(message: any) { closeMessageMenu(); try { await ChatService.SaveAttachmentCopy(message.attachmentId); Message.success('已另存附件') } catch (error: any) { Message.error(error?.message || '另存附件失败') } }
async function openAttachment(message: any) { closeMessageMenu(); try { await ChatService.OpenAttachment(message.attachmentId) } catch (error: any) { Message.error(error?.message || '打开附件失败') } }
async function revealAttachment(message: any) { closeMessageMenu(); try { await ChatService.RevealAttachment(message.attachmentId) } catch (error: any) { Message.error(error?.message || '打开文件夹失败') } }
function attachmentDetailsFallback(message: any): AttachmentDetails {
  return {
    attachmentId: message?.attachmentId || '',
    fileName: message?.attachmentName || message?.content || '未命名附件',
    mimeType: message?.attachmentMime || 'application/octet-stream',
    fileSize: Number(message?.attachmentSize || 0),
    sha256: '',
    status: message?.attachmentStatus || message?.status || 'sending',
    createdAt: message?.createdAt || '',
    localPath: message?.attachmentPath || '',
  }
}
async function showAttachmentDetails(message: any) {
  closeMessageMenu()
  attachmentDetailsMessage.value = message
  attachmentDetails.value = attachmentDetailsFallback(message)
  attachmentDetailsVisible.value = true
  try {
    const details = await ChatService.GetAttachmentDetails(message.attachmentId)
    if (attachmentDetailsMessage.value?.attachmentId === message.attachmentId) attachmentDetails.value = details
  } catch (error: any) {
    // The fallback is intentionally kept visible while a transfer is active.
    console.warn('[FlyQPro] 附件详情暂不可用', error)
  }
}
async function toggleFavorite(message: any) { closeMessageMenu(); const next = !message.isFavorite; try { await ChatService.SetMessageFavorite(message.messageId, next); message.isFavorite = next; Message.success(next ? '已收藏' : '已取消收藏') } catch (error: any) { Message.error(error?.message || '收藏失败') } }
function enterMultiSelect(message: any) { closeMessageMenu(); selectionMode.value = true; selectedMessageIds.add(message.messageId) }
function exitMultiSelect() { selectionMode.value = false; selectedMessageIds.clear() }
async function batchFavorite() { const ids = [...selectedMessageIds]; for (const id of ids) { const message = activeMessages.value.find((item) => item.messageId === id); if (message && !message.isFavorite) { await ChatService.SetMessageFavorite(id, true); message.isFavorite = true } } exitMultiSelect(); Message.success('已收藏所选消息') }
async function batchDelete() { const ids = [...selectedMessageIds]; for (const id of ids) { await ChatService.DeleteMessage(id); delete messagePreviews[id] } if (activePeer.value) await loadConversation(activePeer.value, false, false, false); exitMultiSelect(); Message.success('已删除所选消息') }
function openForward(messages: ChatMessage[], excludedDeviceId = activePeer.value?.deviceId) { const candidates = store.friends.filter((peer) => peer.deviceId !== excludedDeviceId); if (!candidates.length) { Message.warning('没有可转发的好友'); return }; forwardSources.value = messages; forwardCandidates.value = candidates; forwardTargetIds.value = []; forwardVisible.value = true }
function openFavoriteForward(message: ChatMessage) { openForward([message], '') }
async function confirmForward() { if (!forwardTargetIds.value.length) { Message.warning('请选择转发好友'); return }; for (const targetId of forwardTargetIds.value) for (const message of forwardSources.value) await ChatService.SendMessageWithMetadata(targetId, message.content, message.messageId, message.content, message.messageId); Message.success(`已转发给 ${forwardTargetIds.value.length} 位好友`); forwardVisible.value = false; exitMultiSelect() }
function toggleForwardTarget(deviceId: string) { forwardTargetIds.value = forwardTargetIds.value.includes(deviceId) ? forwardTargetIds.value.filter((id) => id !== deviceId) : [...forwardTargetIds.value, deviceId] }
function forwardMessage(message: any) { closeMessageMenu(); openForward([message]) }
function batchForward() { const messages = activeMessages.value.filter((message) => selectedMessageIds.has(message.messageId)); openForward(messages) }
function quoteMessage(message: any) { closeMessageMenu(); quoteMessageId.value = message.messageId; quoteContent.value = message.content || ''; Message.info('已引用消息，请输入回复') }
function closePeerInfo() { showPeerInfo.value = false }
function togglePeerInfo() { showPeerInfo.value = !showPeerInfo.value; if (showPeerInfo.value && activePeer.value) void refreshPeerAvatar(activePeer.value.deviceId) }
function openPeerInfo() { showPeerInfo.value = true; if (activePeer.value) void refreshPeerAvatar(activePeer.value.deviceId) }
function handleMessageAreaPointerDown() { cancelAutoScroll(); markActiveRead() }
function handleMessageAreaClick() { closePeerInfo(); closeAllContextMenus() }
function closeContextMenusOnPointerDown(event: PointerEvent) {
  const target = event.target as Element | null
  if (!target?.closest('.emoji-panel, .emoji-toggle')) emojiOpen.value = false
  if (target?.closest('.message-context-menu, .peer-context-menu, .contact-context-menu, .delete-confirm-popover')) return
  closeAllContextMenus()
}
function handleComposerFocus() { closePeerInfo(); markActiveRead() }
function markActiveRead() { if (!conversationVisible.value || !activePeer.value) return; store.clearConversationUnread(activePeer.value.deviceId); void ChatService.MarkConversationRead(activePeer.value.deviceId) }
function onMessageScroll() { const el = messageScroll.value; if (!el) return; userNearBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 80; if (activePeer.value) { if (userNearBottom.value) localStorage.removeItem(chatScrollKey(activePeer.value.deviceId)); else localStorage.setItem(chatScrollKey(activePeer.value.deviceId), String(Math.max(0, el.scrollTop))) } if (userNearBottom.value) { newMessageCount.value = 0; if (performance.now() >= suppressScrollReadUntil) markActiveRead() } }
function prefersReducedMotion() { return window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false }
function cancelScrollAnimation() { if (scrollScheduleFrame) { cancelAnimationFrame(scrollScheduleFrame); scrollScheduleFrame = 0 }; if (scrollAnimationFrame) { cancelAnimationFrame(scrollAnimationFrame); scrollAnimationFrame = 0 }; scrollAnimationToken++ }
function cancelAutoScroll() { cancelScrollAnimation(); bottomSettleToken++ }
function scrollToBottom(preserveUnread = false, mode: 'instant' | 'animated' = 'instant') {
  const el = messageScroll.value
  if (!el) return
  if (preserveUnread) suppressScrollReadUntil = performance.now() + 250
  newMessageCount.value = 0
  userNearBottom.value = true
  cancelScrollAnimation()
  const target = () => Math.max(0, el.scrollHeight - el.clientHeight)
  if (mode === 'instant' || prefersReducedMotion()) {
    el.scrollTop = target()
    return
  }
  const start = el.scrollTop
  const startedAt = performance.now()
  const token = scrollAnimationToken
  const duration = 150
  const animate = (now: number) => {
    if (token !== scrollAnimationToken) return
    const progress = Math.min(1, (now - startedAt) / duration)
    const eased = 1 - Math.pow(1 - progress, 3)
    el.scrollTop = start + (target() - start) * eased
    if (progress < 1) scrollAnimationFrame = requestAnimationFrame(animate)
    else scrollAnimationFrame = 0
  }
  scrollAnimationFrame = requestAnimationFrame(animate)
}
function scheduleScrollToBottom(preserveUnread = false, mode: 'instant' | 'animated' = 'animated') {
  if (scrollScheduleFrame) cancelAnimationFrame(scrollScheduleFrame)
  scrollScheduleFrame = requestAnimationFrame(() => {
    scrollScheduleFrame = 0
    void nextTick().then(() => scrollToBottom(preserveUnread, mode))
  })
}
function startResize(kind: 'friends' | 'discover' | 'composer', event: PointerEvent) { resizeState = { kind, startX: event.clientX, startY: event.clientY, startValue: kind === 'friends' ? friendsWidth.value : kind === 'discover' ? discoveryWidth.value : composerHeight.value }; window.addEventListener('pointermove', onResize); window.addEventListener('pointerup', stopResize) }
function onResize(event: PointerEvent) { if (!resizeState) return; if (resizeState.kind === 'friends') friendsWidth.value = Math.min(440, Math.max(220, resizeState.startValue + event.clientX - resizeState.startX)); else if (resizeState.kind === 'discover') discoveryWidth.value = Math.min(460, Math.max(240, resizeState.startValue + event.clientX - resizeState.startX)); else composerHeight.value = Math.min(320, Math.max(120, resizeState.startValue - event.clientY + resizeState.startY)) }
function stopResize() { if (!resizeState) return; localStorage.setItem('flyqpro.friendsWidth', String(friendsWidth.value)); localStorage.setItem('flyqpro.discoveryWidth', String(discoveryWidth.value)); localStorage.setItem('flyqpro.composerHeight', String(composerHeight.value)); resizeState = undefined; window.removeEventListener('pointermove', onResize); window.removeEventListener('pointerup', stopResize) }
async function savePeerRemark() { if (!activePeer.value || peerRemark.value === activePeer.value.remark) return; try { await ChatService.SetPeerRemark(activePeer.value.deviceId, peerRemark.value.trim()); const peer = store.peers.find((item) => item.deviceId === activePeer.value?.deviceId); if (peer) peer.remark = peerRemark.value.trim() } catch (error: any) { Message.error(error?.message || '备注保存失败') } }
async function runDiagnostic() { diagnostic.value = await ChatService.RunNetworkDiagnostic() }
function openRepository() { void Browser.OpenURL('https://github.com/gzdzh-cn/FlyQPro') }
function openEmbeddedShared(mode: 'owner' | 'friend', peerId = '') {
  if (section.value === 'shared' && sharedEmbeddedMode.value === mode && sharedEmbeddedDeviceId.value === peerId) return
  const enteringShared = section.value !== 'shared'
  sharedEmbeddedMode.value = mode
  sharedEmbeddedDeviceId.value = peerId
  if (enteringShared) sharedReturnSection.value = 'friends'
  sharedReturnPeerId.value = peerId
  if (enteringShared) switchSection('shared')
  else closeAllContextMenus()
}
function leaveEmbeddedShared() {
  const returnSection = sharedReturnSection.value || 'friends'
  const returnPeer = store.friends.find((peer) => peer.deviceId === sharedReturnPeerId.value)
  switchSection(returnSection)
  if (returnSection === 'friends' && returnPeer) void loadConversation(returnPeer, false, true, true)
}
async function openSharedDrive() {
  try {
    if (store.profile.sharedDriveMultiWindow === true) await SharedDriveWindowService.OpenSharedDrive()
    else openEmbeddedShared('owner')
  } catch (error: any) { Message.error(error?.message || '打开共享窗口失败') }
}
async function openFriendSharedDrive() {
  if (!activePeer.value) return
  if (!activePeerCanSend.value) { Message.warning('当前不是好友，请重新申请好友'); return }
  if (!activePeer.value.online) { Message.warning('好友不在线，暂不支持打开共享盘'); return }
  try {
    if (store.profile.sharedDriveMultiWindow === true) await SharedDriveWindowService.OpenFriendSharedDrive(activePeer.value.deviceId)
    else openEmbeddedShared('friend', activePeer.value.deviceId)
  } catch (error: any) { Message.error(error?.message || '打开好友共享盘失败') }
}
function minimiseWindow() { Window.Minimise() }
async function toggleMaximise() { if (await Window.IsMaximised()) Window.UnMaximise(); else Window.Maximise() }
function closeWindow() { Window.Close() }
watch(() => store.profile, (value) => Object.assign(editProfile, { ...value, nickname: normalizeNickname(value.nickname) }), { deep: true })
watch(() => activePeer.value, (peer) => { peerRemark.value = peer?.remark || '' })
watch(activePeerCanSend, (canSend) => { if (!canSend) emojiOpen.value = false })
watch(activeMessageLoadKey, () => { activeMessages.value.forEach(loadMessagePreview) }, { immediate: true })
watch(activeTransferLoadKey, () => { activeMessages.value.forEach(loadMessagePreview) })
watch(() => store.lastMessageEvent, (message) => {
  if (!message) return
  const isActiveConversation = conversationVisible.value && message.conversationId === `conv-${activePeer.value?.deviceId}`
  if (message.senderDeviceId === deviceInfo.value?.deviceId) {
    if (isActiveConversation && message.status === 'sending') scheduleScrollToBottom(false, 'animated')
    return
  }
  if (isActiveConversation && userNearBottom.value) scheduleScrollToBottom(true, 'animated')
  else if (isActiveConversation) newMessageCount.value += 1
  // Keep the audible cue available while the app is open as well as when it
  // is backgrounded. System banners remain a background-only concern.
  playNotificationTone()
})
watch(() => store.requests, (requests) => {
  const next = new Map(requests.map((request) => [request.requestId, `${request.status}:${request.updatedAt || request.createdAt}`]))
  if (!requestWatchReady) {
    knownRequestStates.clear()
    next.forEach((value, key) => knownRequestStates.set(key, value))
    requestWatchReady = true
    return
  }
  const hasNewPending = requests.some((request) => {
    if (request.status !== 'pending' || request.direction === 'sent') return false
    const state = `${request.status}:${request.updatedAt || request.createdAt}`
    return knownRequestStates.get(request.requestId) !== state
  })
  if (hasNewPending) playNotificationTone()
  knownRequestStates.clear()
  next.forEach((value, key) => knownRequestStates.set(key, value))
}, { deep: true })
watch(() => editProfile.theme, (value) => applyTheme(value))
watch(appBadgeCount, (count) => { void AppBadgeService.SetCount(count).catch(() => undefined) }, { immediate: true })
watch(() => store.peers, () => {
  if (!selectedDiscovery.value) return
  const current = store.peers.find((peer) => peer.deviceId === selectedDiscovery.value?.deviceId)
  if (current) selectedDiscovery.value = current
  else if (!store.discovered.some((peer) => peer.deviceId === selectedDiscovery.value?.deviceId) && !store.contacts.some((peer) => peer.deviceId === selectedDiscovery.value?.deviceId)) selectedDiscovery.value = undefined
}, { deep: true })
onMounted(async () => {
  updateDesktopForeground()
  document.addEventListener('visibilitychange', updateDesktopForeground)
  window.addEventListener('focus', updateDesktopForeground)
  window.addEventListener('blur', updateDesktopForeground)
  window.addEventListener('pointerdown', unlockNotificationAudio, { once: true })
  window.addEventListener('keydown', unlockNotificationAudio, { once: true })
  window.addEventListener('keydown', handleContextMenuKeydown)
  window.addEventListener('pointerdown', closeContextMenusOnPointerDown)
  window.addEventListener('pointerdown', pauseMenuWarmup, { passive: true })
  window.addEventListener('keydown', pauseMenuWarmup)
  cancelNativeDrop = Events.On('chat:file-dropped', (event: any) => {
    const payload = event?.data ?? event ?? {}
    const paths = Array.isArray(payload) ? payload : (payload.filenames || payload.files || [])
    if (activePeerCanSend.value && paths.length) addPendingPaths(paths)
  })
  handleBrowserDrop = (event: Event) => {
    const paths = (event as CustomEvent<{ filenames?: string[] }>).detail?.filenames || []
    if (activePeerCanSend.value && paths.length) addPendingPaths(paths)
  }
  window.addEventListener('flyqpro:file-dropped', handleBrowserDrop)
  try { isMac.value = System.IsMac() } catch { isMac.value = false }
  try { defaultAttachmentPath.value = await ChatService.DefaultAttachmentPath() } catch { defaultAttachmentPath.value = '' }
  await load()
  scheduleMenuWarmup()
})
onBeforeUnmount(() => { saveActiveScrollPosition(); clearMenuWarmupTask(); menuWarmupQueue = []; cancelScrollAnimation(); bottomSettleToken++; document.removeEventListener('visibilitychange', updateDesktopForeground); window.removeEventListener('focus', updateDesktopForeground); window.removeEventListener('blur', updateDesktopForeground); window.removeEventListener('pointerdown', unlockNotificationAudio); window.removeEventListener('keydown', unlockNotificationAudio); window.removeEventListener('keydown', handleContextMenuKeydown); window.removeEventListener('pointerdown', closeContextMenusOnPointerDown); window.removeEventListener('pointerdown', pauseMenuWarmup); window.removeEventListener('keydown', pauseMenuWarmup); if (handleBrowserDrop) window.removeEventListener('flyqpro:file-dropped', handleBrowserDrop); cancelNativeDrop?.(); void notificationAudio?.close() })
</script>

<style scoped lang="less">
.chat-app { height: 100%; display: flex; overflow: hidden; background: #f5f7fb; color: #1d2129; }
.rail { width: 76px; flex: 0 0 76px; background: #17233c; display: flex; align-items: center; flex-direction: column; padding: 22px 10px 16px; box-sizing: border-box; color: #c9d4e8; }
.profile-button, .rail-nav button, .rail-settings { border: 0; background: transparent; color: inherit; cursor: pointer; border-radius: 14px; }
.profile-button { padding: 0; margin-bottom: 28px; }.rail-nav { display: flex; flex-direction: column; gap: 10px; align-items: center; flex: 1; }.rail-nav button, .rail-settings { width: 54px; height: 58px; position: relative; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 4px; }.rail-nav button span, .rail-settings span, .rail-nav button svg, .rail-settings svg { font-size: 22px; width: 22px; height: 22px; line-height: 22px; }.rail-nav small, .rail-settings small { font-size: 11px; }.rail-nav button.active, .rail-settings.active { color: #fff; background: #2e5bba; }.rail-nav b { position: absolute; top: 2px; right: 5px; min-width: 16px; height: 16px; border-radius: 9px; background: #f53f3f; color: #fff; font-size: 10px; line-height: 16px; }
.avatar { width: 44px; height: 44px; border-radius: 14px; color: #fff; display: flex; align-items: center; justify-content: center; font-weight: 700; position: relative; flex: 0 0 auto; }.avatar.large { width: 46px; height: 46px; border-radius: 15px; }.avatar.huge { width: 92px; height: 92px; border-radius: 28px; font-size: 30px; }.avatar i { position: absolute; width: 10px; height: 10px; border: 2px solid #fff; border-radius: 50%; background: #86909c; bottom: -1px; right: -1px; }.avatar i.online { background: #00b42a; }
.workspace { flex: 1; display: flex; min-width: 0; }.list-pane { width: 290px; flex: 0 0 290px; background: #fff; border-right: 1px solid #e5e6eb; display: flex; flex-direction: column; }.pane-title { padding: 26px 20px 18px; display: flex; justify-content: space-between; align-items: center; }.pane-title div { display: flex; align-items: baseline; gap: 8px; }.pane-title strong { font-size: 22px; }.pane-title span { color: #86909c; font-size: 13px; }.icon-button { border: 0; background: transparent; cursor: pointer; color: #4e5969; font-size: 22px; }.search { margin: 0 16px 14px; width: calc(100% - 32px); }.list-scroll { flex: 1; overflow: auto; padding: 0 0 20px; }.peer-row, .request-row { width: 100%; box-sizing: border-box; border: 0; background: transparent; text-align: left; display: flex; align-items: center; gap: 12px; padding: 11px 20px; border-radius: 0; cursor: pointer; }.peer-row:hover, .request-row:hover, .peer-row.selected, .request-row.selected { background: #f2f5ff; }.peer-copy, .request-row > div:last-child { display: flex; flex-direction: column; gap: 4px; min-width: 0; }.peer-copy strong, .request-row strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.peer-copy span, .request-row span { font-size: 12px; color: #86909c; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.empty-small { text-align: center; color: #86909c; padding: 90px 20px; }.empty-icon, .brand-mark { font-size: 42px; color: #4e7cff; }.conversation, .detail-pane, .blank-state { flex: 1; min-width: 0; display: flex; flex-direction: column; }.conversation-head { height: 76px; flex: 0 0 76px; background: #fff; border-bottom: 1px solid #e5e6eb; padding: 0 28px; display: flex; align-items: center; justify-content: space-between; }.head-peer { display: flex; gap: 12px; align-items: center; }.head-peer > div:last-child { display: flex; flex-direction: column; gap: 4px; }.head-peer span { font-size: 12px; color: #86909c; }.onlineText { color: #00b42a !important; }.message-scroll { flex: 1; overflow: auto; padding: 28px 12%; }.message-line { display: flex; margin: 12px 0; }.message-line.mine { justify-content: flex-end; }.message-bubble { max-width: 65%; padding: 11px 15px; border-radius: 16px 16px 16px 4px; background: #fff; box-shadow: 0 4px 16px rgba(28, 49, 93, .05); white-space: pre-wrap; line-height: 1.55; }.message-line.mine .message-bubble { color: #fff; background: #3767e8; border-radius: 16px 16px 4px 16px; }.message-bubble small { display: block; opacity: .65; font-size: 10px; margin-top: 5px; }.conversation-empty, .blank-state { align-items: center; justify-content: center; color: #86909c; }.conversation-empty h3, .conversation-empty p { margin: 4px; }.blank-state h2, .blank-state p { margin: 7px; }.composer { padding: 12px 24px 18px; background: #fff; border-top: 1px solid #e5e6eb; }.composer-tools { height: 28px; display: flex; align-items: center; gap: 10px; }.composer-tools button { border: 0; background: transparent; color: #4e5969; font-size: 18px; cursor: pointer; }.picked-file { color: #4e7cff; font-size: 12px; }.composer textarea { display: block; width: 100%; min-height: 68px; border: 0; outline: none; resize: none; font-size: 14px; padding: 8px 0; box-sizing: border-box; }.composer-foot { display: flex; align-items: center; justify-content: space-between; color: #86909c; font-size: 12px; }.info-pane { width: 280px; flex: 0 0 280px; background: #fff; border-left: 1px solid #e5e6eb; padding: 24px 20px; }.info-head { display: flex; justify-content: space-between; }.info-profile { text-align: center; padding: 30px 0 24px; }.info-profile .avatar { margin: auto; }.info-profile h3 { margin: 12px 0 4px; }.info-profile span { color: #00b42a; font-size: 12px; }.info-fields, .basic-info, .device-fields { display: flex; flex-direction: column; gap: 18px; }.info-fields label, .basic-info label, .device-fields label { color: #86909c; font-size: 12px; display: flex; flex-direction: column; gap: 5px; }.info-fields strong, .basic-info strong, .device-fields strong { color: #1d2129; font-weight: 500; word-break: break-all; }.mono { font-family: monospace; font-size: 11px; }.discovery-pane { width: 320px; flex-basis: 320px; }.group-title { border: 0; background: transparent; display: flex; justify-content: space-between; width: 100%; padding: 14px 20px 7px; cursor: pointer; color: #4e5969; font-weight: 600; }.group-title b { background: #e8f3ff; color: #165dff; padding: 1px 7px; border-radius: 10px; }.request-row { padding: 12px 18px; }.detail-pane { overflow: auto; align-items: center; justify-content: center; padding: 40px; box-sizing: border-box; }.detail-card { width: min(440px, 100%); background: #fff; border-radius: 20px; padding: 42px; box-sizing: border-box; text-align: center; box-shadow: 0 16px 50px rgba(32, 56, 99, .08); }.detail-card .avatar { margin: auto; }.detail-card h2 { margin: 18px 0 8px; }.detail-card p { color: #4e5969; line-height: 1.6; }.detail-actions { display: flex; justify-content: center; gap: 12px; margin-top: 26px; }.subtle { color: #86909c; font-size: 12px; }.tags { display: flex; justify-content: center; gap: 8px; margin: 16px; }.basic-info { text-align: left; padding: 18px 0 25px; }.settings-shell { flex: 1; overflow: auto; }.settings-head { padding: 30px 52px 0; background: #fff; }.settings-head h2 { margin: 0 0 6px; font-size: 26px; }.settings-head p { color: #86909c; margin: 0 0 24px; }.settings-tabs { display: flex; gap: 25px; }.settings-tabs button { border: 0; background: transparent; padding: 12px 2px; color: #86909c; cursor: pointer; border-bottom: 2px solid transparent; }.settings-tabs button.active { color: #165dff; border-color: #165dff; }.settings-content { max-width: 900px; padding: 28px 52px 60px; }.setting-card { background: #fff; border-radius: 16px; padding: 24px 28px; margin-bottom: 16px; }.setting-card h3 { margin: 0 0 16px; }.profile-card, .device-card { display: flex; gap: 28px; align-items: center; }.profile-edit { flex: 1; }.profile-edit p { color: #86909c; font-size: 12px; }.setting-line { min-height: 58px; border-top: 1px solid #f2f3f5; display: flex; align-items: center; justify-content: space-between; gap: 20px; }.setting-line > div { display: flex; flex-direction: column; gap: 5px; }.setting-line span { color: #86909c; font-size: 12px; }.path { max-width: 550px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.network-summary { display: flex; align-items: center; gap: 14px; }.network-summary > div:nth-child(2) { flex: 1; display: flex; flex-direction: column; gap: 5px; }.network-summary span { color: #86909c; font-size: 12px; }.network-dot { width: 12px; height: 12px; border-radius: 50%; background: #00b42a; }.network-dot.warning { background: #ff7d00; }.network-dot.error { background: #f53f3f; }.diagnostic-list { margin-top: 24px; border-top: 1px solid #f2f3f5; }.diagnostic-row { display: flex; align-items: center; gap: 12px; padding: 13px 0; border-bottom: 1px solid #f2f3f5; }.diagnostic-icon { width: 20px; height: 20px; border-radius: 50%; text-align: center; line-height: 20px; color: #fff; background: #00b42a; }.diagnostic-icon.error { background: #f53f3f; }.diagnostic-row div { display: flex; flex-direction: column; gap: 3px; }.diagnostic-row span:last-child { font-size: 12px; color: #86909c; }.about-card { text-align: center; padding: 60px; }.about-card .brand-mark { font-size: 60px; }.about-rows { max-width: 380px; margin: 25px auto; text-align: left; }.about-rows span { display: flex; justify-content: space-between; padding: 12px 0; border-bottom: 1px solid #f2f3f5; }.about-rows strong { color: #1d2129; font-weight: 500; }
.message-scroll { display: flex; flex-direction: column; }
.message-scroll { position: relative; }
.message-line.is-selected .message-bubble { outline: 2px solid #3767e8; outline-offset: 3px; }
.message-bubble.is-favorite::before { content: '★'; position: absolute; right: -18px; top: -8px; color: #ffb400; font-size: 13px; }
.message-bubble { position: relative; }
.message-quote { margin: -2px 0 8px; padding: 5px 8px; border-left: 3px solid rgba(128, 145, 180, .7); color: var(--muted); font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.attachment-complete-actions { display: flex; gap: 6px; margin-top: 8px; padding-top: 7px; border-top: 1px solid rgba(128, 145, 180, .18); }
.attachment-complete-actions button, .message-context-menu button { border: 0; background: transparent; color: inherit; cursor: pointer; font-size: 12px; padding: 4px 7px; border-radius: 5px; }
.attachment-complete-actions button:hover, .message-context-menu button:hover { background: rgba(55, 103, 232, .12); }
.message-context-menu, .peer-context-menu { position: fixed; z-index: 9999; min-width: 150px; padding: 6px; display: flex; flex-direction: column; opacity: 1; pointer-events: auto; background: var(--surface-1); color: var(--text); border: 1px solid var(--line); border-radius: 9px; box-shadow: 0 12px 30px rgba(20, 30, 60, .28); }
.message-context-menu button { text-align: left; }
.peer-context-menu button { border: 0; background: transparent; color: inherit; cursor: pointer; text-align: left; font-size: 13px; padding: 8px 10px; border-radius: 6px; }
.peer-context-menu button:hover { background: rgba(55, 103, 232, .12); }
.peer-context-menu button.danger { color: #f53f3f; }
.message-context-menu button:disabled { opacity: .4; cursor: not-allowed; }
.message-context-menu button.danger { color: #f53f3f; }
.selection-toolbar { position: absolute; z-index: 20; left: 50%; top: 12px; transform: translateX(-50%); display: flex; align-items: center; gap: 8px; padding: 7px 10px; background: var(--surface-1); border: 1px solid var(--line); border-radius: 9px; box-shadow: 0 7px 20px rgba(20, 30, 60, .12); }
.file-attachment-content { width: 100%; min-width: 0; box-sizing: border-box; }
.file-attachment-title { display: flex; align-items: center; gap: 5px; min-width: 0; max-width: 100%; }
.file-attachment-title svg { flex: 0 0 auto; }
.file-attachment-title span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.attachment-meta { display: flex; align-items: center; min-width: 0; height: 18px; margin-top: 5px; color: var(--attachment-meta); font-size: 12px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.attachment-percent { display: inline-block; width: 4ch; color: var(--attachment-percent); font-weight: 600; text-align: left; }
.chat-app .message-line.mine .attachment-meta { color: var(--attachment-meta-outgoing); }
.chat-app .message-line.mine .attachment-percent { color: var(--attachment-percent-outgoing); }
.attachment-details { display: grid; grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr); gap: 12px 18px; color: var(--text); }
.attachment-details p { display: flex; gap: 14px; margin: 0; align-items: flex-start; }
.attachment-details p span { width: 76px; flex: 0 0 76px; color: var(--muted); }
.attachment-details p strong { min-width: 0; word-break: break-word; }
.attachment-details-hero { display: flex; grid-column: 1 / -1; flex-direction: column; gap: 4px; padding: 0 0 2px; border: 0; border-radius: 0; background: transparent; }
.attachment-details-hero-heading { display: flex; align-items: center; gap: 8px; min-width: 0; min-height: 24px; }
.attachment-details-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 14px; }
.attachment-details-section { display: flex; min-width: 0; flex-direction: column; gap: 8px; padding-top: 2px; }
.attachment-details-file-section { grid-column: 1 / -1; }
.attachment-details-section h4 { margin: 0; color: var(--text); font-size: 12px; }
.attachment-details-grid { display: grid; min-width: 0; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px 16px; }
.attachment-details-grid p { min-width: 0; max-width: 100%; display: flex; flex-direction: column; gap: 3px; overflow: hidden; }
.attachment-details-grid p span { width: auto; flex: 0 0 auto; font-size: 11px; }
.attachment-details-grid p strong { display: block; min-width: 0; max-width: 100%; overflow: hidden; font-size: 12px; font-variant-numeric: tabular-nums; text-overflow: ellipsis; white-space: nowrap; }
.attachment-details-rate { display: flex !important; flex-direction: column; align-items: flex-start; gap: 1px; line-height: 1.15; white-space: normal !important; }
.attachment-details-rate span,
.attachment-details-rate small { display: block; min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.attachment-details-rate small { color: var(--muted); font-size: 10px; font-weight: 500; }
.attachment-details-wide { grid-column: 1 / -1; }
.attachment-details-ellipsis { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.attachment-details-reason { margin: 0; padding: 7px 9px; border-radius: 6px; background: color-mix(in srgb, var(--accent) 8%, transparent); color: var(--muted); font-size: 11px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.attachment-transfer-details-action { display: flex; justify-content: flex-end; margin-top: 5px; }
.attachment-transfer-details-action button { padding: 0; border: 0; background: transparent; color: var(--accent); font-size: 11px; cursor: pointer; }
.forward-targets { display: flex; flex-direction: column; gap: 12px; }
.conversation-empty { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; box-sizing: border-box; padding: 18px 14px; }
.info-danger { margin-top: auto; padding-top: 24px; display: flex; flex-direction: column; gap: 8px; }.info-danger span { color: #86909c; font-size: 11px; line-height: 1.5; }
.self-avatar-preview { display: flex; flex-direction: column; align-items: center; gap: 14px; padding: 18px 0 8px; }.self-avatar-preview .avatar { width: 220px; height: 220px; border-radius: 34px; font-size: 72px; }
.self-profile-card { color: var(--text); padding: 4px 2px 2px; }
.self-profile-heading { display: flex; align-items: center; gap: 14px; padding: 4px 4px 18px; border-bottom: 1px solid var(--line); }
.self-profile-heading .avatar { width: 66px; height: 66px; border-radius: 20px; font-size: 24px; }
.self-profile-name { min-width: 0; display: flex; flex-direction: column; gap: 6px; }
.self-profile-name strong { font-size: 20px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.self-profile-name span, .self-profile-hint { color: var(--muted); font-size: 12px; }
.self-profile-body { display: flex; align-items: center; gap: 22px; padding: 20px 4px 12px; }
.profile-qr-box { width: 276px; height: 276px; flex: 0 0 276px; display: flex; align-items: center; justify-content: center; border: 1px solid var(--line); border-radius: 12px; background: #fff; overflow: hidden; }
.profile-qr-box img { display: block; width: 260px; height: 260px; image-rendering: pixelated; }
.profile-qr-loading { color: #86909c; font-size: 12px; }
.self-profile-fields { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 13px; }
.self-profile-fields div { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.self-profile-fields span { color: var(--muted); font-size: 11px; }
.self-profile-fields strong { color: var(--text); font-size: 13px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.self-profile-hint { margin: 4px 4px 0; text-align: center; }
.profile-buttons { display: flex; gap: 8px; flex-wrap: wrap; }
.attachment-actions { display: flex; gap: 6px; margin-top: 8px; }
.request-copy { display: flex; flex-direction: column; gap: 4px; min-width: 0; flex: 1; }
.attachment-pending { display: flex; align-items: center; justify-content: flex-end; gap: 10px; margin-top: 8px; color: var(--muted); font-size: 11px; }
.attachment-pending-actions { display: inline-flex; align-items: center; gap: 8px; }
.chat-app .message-line.mine .attachment-pending { color: var(--message-outgoing-text); font-weight: 600; }
.chat-app .message-line.mine .attachment-pending span { text-shadow: 0 1px 2px rgba(20, 38, 84, .24); }

.chat-app.theme-dark { background: #101827; color: #e5e7eb; }
.chat-app.theme-dark .conversation-head,
.chat-app.theme-dark .composer,
.chat-app.theme-dark .info-pane,
.chat-app.theme-dark .settings-head,
.chat-app.theme-dark .setting-card,
.chat-app.theme-dark .detail-card { background: #182235; color: #e5e7eb; }
.chat-app.theme-dark .message-scroll,
.chat-app.theme-dark .detail-pane { background: #101827; }
.chat-app.theme-dark .list-pane,
.chat-app.theme-dark .conversation-head,
.chat-app.theme-dark .composer,
.chat-app.theme-dark .info-pane { border-color: #2c394e; }
.chat-app.theme-dark .pane-title span,
.chat-app.theme-dark .peer-copy span,
.chat-app.theme-dark .request-row span,
.chat-app.theme-dark .head-peer span,
.chat-app.theme-dark .settings-head p,
.chat-app.theme-dark .profile-edit p,
.chat-app.theme-dark .setting-line span,
.chat-app.theme-dark .subtle,
.chat-app.theme-dark .detail-card p,
.chat-app.theme-dark .composer-foot,
.chat-app.theme-dark .diagnostic-row span:last-child { color: #a9b5c7; }
.chat-app.theme-dark .icon-button,
.chat-app.theme-dark .composer-tools button,
.chat-app.theme-dark .settings-tabs button,
.chat-app.theme-dark .group-title { color: #c5d0df; }
.chat-app.theme-dark .peer-row:hover,
.chat-app.theme-dark .request-row:hover,
.chat-app.theme-dark .peer-row.selected,
.chat-app.theme-dark .request-row.selected { background: #263653; }
.chat-app.theme-dark .message-bubble { background: #253249; color: #e5e7eb; box-shadow: 0 4px 16px rgba(0, 0, 0, .18); }
.chat-app.theme-dark .message-context-menu, .chat-app.theme-dark .peer-context-menu { background: #253249; color: #e5e7eb; border-color: #465875; box-shadow: 0 16px 38px rgba(0, 0, 0, .48); }
.chat-app.theme-dark .composer textarea { background: transparent; color: #e5e7eb; }
.chat-app.theme-dark .composer textarea::placeholder { color: #8492a6; }
.chat-app.theme-dark .info-fields strong,
.chat-app.theme-dark .basic-info strong,
.chat-app.theme-dark .device-fields strong,
.chat-app.theme-dark .about-rows strong { color: #e5e7eb; }
.chat-app.theme-dark .setting-line,
.chat-app.theme-dark .about-rows span,
.chat-app.theme-dark .diagnostic-list,
.chat-app.theme-dark .diagnostic-row { border-color: #2c394e; }
.chat-app.theme-dark .group-title b { background: #203c69; color: #8db5ff; }
.chat-app.theme-dark .detail-card { box-shadow: 0 16px 50px rgba(0, 0, 0, .24); }

/* Final surface system: the app is intentionally divided into distinct layers. */
.chat-app:not(.theme-dark) {
  --app-bg: var(--fp-light-page);
  --surface-1: var(--fp-light-surface);
  --surface-2: var(--fp-light-muted-surface);
  --surface-3: #dde3e9;
  --surface-4: #d3dae2;
  --line: var(--fp-light-line);
  --text: var(--fp-light-text);
  --muted: var(--fp-light-muted);
  --attachment-meta: #4b5f78;
  --attachment-percent: #1f4fb8;
  --attachment-meta-outgoing: rgba(255, 255, 255, .86);
  --attachment-percent-outgoing: #ffffff;
  --hover: var(--fp-light-hover);
  --list-bg: #f1f3f6;
  --accent: #5c7398;
  --shadow: 0 12px 30px rgba(37, 48, 62, .08);
  --message-incoming: #e5ebf2;
  --message-outgoing: #3767e8;
  --message-outgoing-text: #ffffff;
  --scroll-track: #edf0f3;
  --scroll-thumb: #b7c0cb;
}
.chat-app.theme-dark {
  --app-bg: var(--fp-dark-page);
  --surface-1: var(--fp-dark-surface);
  --surface-2: var(--fp-dark-muted-surface);
  --surface-3: #242a32;
  --surface-4: #2d343d;
  --line: var(--fp-dark-line);
  --text: var(--fp-dark-text);
  --muted: var(--fp-dark-muted);
  --attachment-meta: #c5d3e6;
  --attachment-percent: #c5d8ff;
  --attachment-meta-outgoing: rgba(247, 250, 255, .9);
  --attachment-percent-outgoing: #ffffff;
  --hover: var(--fp-dark-hover);
  --list-bg: #202428;
  --accent: #7897d0;
  --shadow: 0 14px 36px rgba(0, 0, 0, .28);
  --message-incoming: #252d38;
  --message-outgoing: #3f639d;
  --message-outgoing-text: #f7faff;
  --scroll-track: #15181d;
  --scroll-thumb: #46515d;
}
.chat-app,
.chat-app .workspace,
.chat-app .settings-shell { background: var(--app-bg); color: var(--text); }
.chat-app .list-pane,
.chat-app .conversation-head,
.chat-app .composer,
.chat-app .info-pane,
.chat-app .settings-head,
.chat-app .settings-nav,
.chat-app .setting-card,
.chat-app .detail-card { background: var(--surface-1); color: var(--text); border-color: var(--line); }
.chat-app .message-scroll,
.chat-app .detail-pane,
.chat-app .settings-panel { background: var(--app-bg); }
.chat-app .list-pane { border-right-color: var(--line); }
.chat-app .conversation-head,
.chat-app .composer { border-color: var(--line); }
.chat-app .peer-row:hover,
.chat-app .request-row:hover,
.chat-app .peer-row.selected,
.chat-app .request-row.selected { background: var(--hover); }
.chat-app .request-row { color: var(--text); }
.chat-app .request-row strong { color: var(--text); font-weight: 600; }
.chat-app .request-row span { color: var(--muted); }
.chat-app .peer-row { color: var(--text); }
.chat-app .peer-row strong { color: var(--text); font-weight: 600; }
.chat-app .peer-row span { color: var(--muted); }
.chat-app .message-bubble { background: var(--message-incoming); color: var(--text); box-shadow: var(--shadow); }
.chat-app .message-line.mine .message-bubble { background: var(--message-outgoing); color: var(--message-outgoing-text); }
.chat-app .composer textarea { background: transparent; color: var(--text); }
.chat-app .composer textarea::placeholder { color: var(--muted); }
.chat-app .composer.composer-disabled { background: var(--surface-2); }
.chat-app .composer textarea:disabled {
  background: var(--surface-3);
  color: var(--muted);
  cursor: not-allowed;
  border-radius: 8px;
  padding-left: 10px;
  padding-right: 10px;
  opacity: 1;
}
.chat-app .composer.composer-disabled .composer-tools button { color: var(--muted); opacity: .45; cursor: not-allowed; }
.chat-app .composer.composer-disabled .composer-foot { color: var(--muted); }
.chat-app .pane-title span,
.chat-app .peer-copy span,
.chat-app .request-row span,
.chat-app .head-peer span,
.chat-app .settings-head p,
.chat-app .profile-edit p,
.chat-app .setting-line span,
.chat-app .subtle,
.chat-app .detail-card p,
.chat-app .composer-foot { color: var(--muted); }
.chat-app .info-fields strong,
.chat-app .basic-info strong,
.chat-app .device-fields strong,
.chat-app .about-rows strong { color: var(--text); }
.chat-app .setting-line,
.chat-app .about-rows span,
.chat-app .diagnostic-list,
.chat-app .diagnostic-row { border-color: var(--line); }

.settings-shell { display: flex; flex-direction: column; min-width: 0; min-height: 0; overflow: hidden; }
.settings-head { flex: 0 0 auto; padding: 28px 34px 22px; border-bottom: 1px solid var(--line); }
.settings-head h2 { margin: 0 0 6px; color: var(--text); font-size: 25px; }
.settings-head p { margin: 0; color: var(--muted); }
.settings-layout { flex: 1; min-height: 0; min-width: 0; display: grid; grid-template-columns: 226px minmax(0, 1fr); }
.settings-nav { min-width: 0; padding: 18px 12px; border-right: 1px solid var(--line); }
.settings-nav button { width: 100%; display: grid; grid-template-columns: 30px minmax(0, 1fr); grid-template-rows: 22px 18px; column-gap: 8px; align-items: center; border: 0; border-radius: 12px; padding: 12px 12px; margin-bottom: 7px; background: transparent; color: var(--muted); text-align: left; cursor: pointer; transition: background .18s ease, color .18s ease; }
.settings-nav button > span { grid-row: 1 / span 2; align-self: center; text-align: center; font-size: 20px; color: currentColor; }
.settings-nav button strong { color: inherit; font-size: 14px; font-weight: 600; }
.settings-nav button small { color: inherit; font-size: 11px; line-height: 16px; opacity: .8; }
.settings-nav button:hover { background: var(--hover); color: var(--text); }
.settings-nav button.active { background: #315fbd; color: #fff; box-shadow: 0 6px 16px rgba(49, 95, 189, .24); }
.settings-nav button.active small { color: #dce8ff; }
.settings-panel { min-width: 0; min-height: 0; overflow: auto; }
.settings-content { width: 100%; max-width: none; box-sizing: border-box; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: start; gap: 18px; padding: 28px 34px 56px; }
.settings-content > .setting-card { min-width: 0; margin: 0; }
.settings-content > .profile-card,
.settings-content > .network-card,
.settings-content > .device-card { grid-column: 1 / -1; }
.settings-content > .network-card + .setting-card { grid-column: 1 / -1; }
.setting-card { border: 1px solid var(--line); border-radius: 14px; box-shadow: none; }
.settings-content .setting-card h3 { color: var(--text); }
.profile-card { min-height: 154px; }
.device-card { display: block; min-height: 190px; }
.device-card .device-fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 22px 42px; padding: 10px 8px; }
.device-card .device-fields label { min-width: 0; }
.about-card { width: min(760px, 100%); justify-self: center; }

.chat-app.theme-dark .settings-nav,
.chat-app.theme-dark .settings-panel,
.chat-app.theme-dark .settings-head { background: var(--surface-1); }
.chat-app.theme-dark .settings-panel { background: var(--app-bg); }
.chat-app.theme-dark .setting-card { background: var(--surface-2); }
.chat-app.theme-dark .setting-card:hover { border-color: #4a5664; }
.chat-app.theme-dark .settings-nav button.active { background: #426fc9; }
.chat-app.theme-dark .settings-nav button:hover:not(.active) { background: var(--hover); }
.chat-app.theme-dark :deep(.arco-input-wrapper),
.chat-app.theme-dark :deep(.arco-select-view),
.chat-app.theme-dark :deep(.arco-textarea-wrapper) { background: var(--surface-3); border-color: var(--line); color: var(--text); }
.chat-app.theme-dark :deep(.arco-input),
.chat-app.theme-dark :deep(.arco-textarea),
.chat-app.theme-dark :deep(.arco-select-view-value) { color: var(--text); }
.chat-app.theme-dark :deep(.arco-input::placeholder),
.chat-app.theme-dark :deep(.arco-textarea::placeholder) { color: var(--muted); }
:global(body.flyqpro-dark .arco-trigger-popup),
:global(body.flyqpro-dark .arco-select-popup),
:global(body.flyqpro-dark .arco-modal) { background: #1b2027; color: #f0f2f5; border-color: #39424d; }
:global(body.flyqpro-dark .arco-modal-container) { background: transparent; }
:global(.arco-modal.attachment-details-modal) { --surface-1: var(--fp-light-surface, #fff); --surface-2: var(--fp-light-muted-surface, #f5f6f8); --text: var(--fp-light-text, #1d2129); --muted: var(--fp-light-muted, #86909c); --line: var(--fp-light-line, #e5e6eb); --accent: #3767e8; max-height: calc(100vh - 40px); overflow: hidden; background: var(--surface-1); color: var(--text); border: 1px solid var(--line); }
:global(.arco-modal.attachment-details-modal .arco-modal-header),
:global(.arco-modal.attachment-details-modal .arco-modal-body) { background: var(--surface-1); color: var(--text); border-color: var(--line); }
:global(.arco-modal.attachment-details-modal .arco-modal-body) { max-height: none; overflow: hidden; }
:global(body.flyqpro-dark .arco-modal.attachment-details-modal) { --surface-1: var(--fp-dark-surface, #1b2027); --surface-2: var(--fp-dark-muted-surface, #252d38); --text: var(--fp-dark-text, #f0f2f5); --muted: var(--fp-dark-muted, #a9b5c7); --line: var(--fp-dark-line, #39424d); --accent: #7897d0; background: var(--surface-1); color: var(--text); border-color: var(--line); box-shadow: 0 18px 54px rgba(0, 0, 0, .45); }
:global(body.flyqpro-dark .arco-modal.attachment-details-modal .arco-modal-header),
:global(body.flyqpro-dark .arco-modal.attachment-details-modal .arco-modal-body) { background: var(--surface-1); color: var(--text); border-color: var(--line); }

@media (max-width: 540px) {
  .message-bubble.attachment-bubble { width: min(340px, calc(100vw - 40px)); max-width: min(340px, calc(100vw - 40px)); }
  .attachment-details { grid-template-columns: minmax(0, 1fr); }
  .attachment-details-grid { grid-template-columns: minmax(0, 1fr); }
  .attachment-details-file-section { grid-column: auto; }
  .attachment-details-wide { grid-column: auto; }
}

.migration-lock { position: fixed; inset: 0; z-index: 1000; display: flex; align-items: center; justify-content: center; background: rgba(12, 18, 28, .62); backdrop-filter: blur(3px); cursor: wait; }
.migration-card { width: min(460px, calc(100vw - 48px)); padding: 30px 34px; border: 1px solid var(--line); border-radius: 14px; background: var(--surface-1); color: var(--text); box-shadow: 0 24px 80px rgba(0, 0, 0, .28); text-align: center; cursor: default; }
.migration-card h3 { margin: 12px 0 8px; font-size: 20px; }
.migration-card p { margin: 10px 0; color: var(--muted); font-size: 13px; }
.migration-path, .migration-file { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.migration-spinner { color: var(--accent); font-size: 34px; animation: migration-spin 1s linear infinite; }
.migration-success { color: #00b42a; font-size: 34px; }
.migration-error { color: #f53f3f; font-size: 34px; }
.migration-error-text { color: #f53f3f !important; }
.is-locked { pointer-events: none; }
.avatar-upload { position: relative; border: 0; padding: 0; background: transparent; cursor: pointer; border-radius: 28px; }
.avatar-camera { position: absolute; right: 2px; bottom: 2px; width: 28px; height: 28px; display: flex; align-items: center; justify-content: center; border-radius: 50%; background: var(--accent); color: #fff; opacity: 0; transition: opacity .16s ease; }
.avatar-upload:hover .avatar-camera, .avatar-upload:focus-visible .avatar-camera { opacity: 1; }
.path-actions { display: flex !important; flex-direction: row !important; gap: 8px; }
@keyframes migration-spin { to { transform: rotate(360deg); } }

@media (max-width: 1050px) {
  .settings-layout { grid-template-columns: 196px minmax(0, 1fr); }
  .settings-content { padding: 24px 24px 48px; gap: 14px; }
}
@media (max-width: 860px) {
  .settings-layout { grid-template-columns: 176px minmax(0, 1fr); }
  .settings-content { grid-template-columns: minmax(0, 1fr); }
  .settings-content > .setting-card { grid-column: 1; }
  .device-card .device-fields { grid-template-columns: minmax(0, 1fr); }
}

/* Compact, unified navigation and the original top-level settings tabs. */
.rail { width: 64px; flex-basis: 64px; padding: 18px 7px 14px; background: var(--surface-2); color: var(--muted); border-right: 1px solid var(--line); }
.profile-button, .rail-nav button, .rail-settings { color: var(--muted); }
.rail-nav button, .rail-settings { width: 50px; height: 54px; }
.rail-nav button.active, .rail-settings.active { color: #fff; background: var(--accent); box-shadow: 0 5px 14px rgba(73, 109, 182, .18); }
.rail-nav button:hover:not(.active), .rail-settings:hover:not(.active), .profile-button:hover { background: var(--hover); color: var(--text); }
.settings-head { display: flex; align-items: flex-end; justify-content: space-between; gap: 24px; }
.settings-tabs { display: flex; align-items: flex-end; gap: 26px; }
.settings-tabs button { color: var(--muted); }
.settings-tabs button:hover { color: var(--text); }
.settings-tabs button.active { color: var(--accent); border-color: var(--accent); }
.settings-panel { flex: 1; min-height: 0; min-width: 0; overflow: auto; }
.settings-content { grid-template-columns: repeat(2, minmax(0, 1fr)); width: min(100%, 1320px); margin: 0 auto; }
.chat-app.theme-dark .settings-head { background: var(--surface-2); }
.chat-app.theme-dark .settings-tabs button.active { color: #8eafff; border-color: #8eafff; }
.chat-app.theme-dark .rail { background: var(--surface-2); }
.chat-app.theme-dark .rail-nav button.active, .chat-app.theme-dark .rail-settings.active { background: var(--accent); }

@media (min-width: 1250px) {
  .settings-content { padding-left: 48px; padding-right: 48px; }
}

/* Return settings to the original single-column rhythm, but center it in the available window. */
.chat-app .list-pane,
.chat-app .discovery-pane { background: var(--list-bg); }
.settings-content { grid-template-columns: minmax(0, 1fr); width: min(100%, 980px); padding: 28px 40px 56px; gap: 16px; }
.settings-content > .setting-card,
.settings-content > .profile-card,
.settings-content > .network-card,
.settings-content > .device-card,
.settings-content > .network-card + .setting-card { grid-column: auto; }
.settings-content > .setting-card { width: 100%; }
.device-card .device-fields { grid-template-columns: minmax(0, 1fr); max-width: 720px; margin: 0 auto; }
.about-card { width: min(100%, 760px); margin-left: auto; margin-right: auto; }

@media (max-width: 760px) {
  .settings-head { align-items: flex-start; flex-direction: column; gap: 12px; }
  .settings-tabs { width: 100%; justify-content: space-between; gap: 12px; overflow-x: auto; }
  .settings-content { padding-left: 24px; padding-right: 24px; }
  .device-card { padding: 24px 22px 20px; }
  .device-card .device-fields { grid-template-columns: minmax(0, 1fr); }
}

/* The about page is a centered card, not a full-window colored panel. */
.chat-app .settings-panel,
.chat-app .settings-content,
.chat-app .settings-content > .setting-card { background: var(--surface-1); }
.chat-app.theme-dark .setting-card,
.chat-app.theme-dark .settings-content > .about-card { background: var(--surface-1); }
.settings-content { width: min(1180px, calc(100% - clamp(32px, 7vw, 120px))); max-width: none; padding: 28px 0 56px; }
.settings-content > .about-card { width: min(760px, 100%); justify-self: center; }
.about-card { min-height: 0; padding: 44px 52px; }

/* Device identity is a content card now that the profile avatar is no longer part of this page. */
.device-card { display: flex; flex-direction: column; gap: 24px; min-height: 0; padding: 28px 32px 22px; }
.device-card-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 24px; }
.device-eyebrow { display: block; margin-bottom: 7px; color: var(--accent); font-size: 11px; font-weight: 700; letter-spacing: .08em; text-transform: uppercase; }
.device-card-head h3 { margin: 0 0 6px; font-size: 19px; }
.device-card-head p { margin: 0; color: var(--muted); font-size: 12px; }
.device-badge { display: inline-flex; align-items: center; gap: 7px; flex: 0 0 auto; padding: 7px 11px; border: 1px solid color-mix(in srgb, var(--accent) 28%, var(--line)); border-radius: 999px; color: var(--accent); font-size: 12px; font-weight: 600; }
.device-badge i { width: 7px; height: 7px; border-radius: 50%; background: #00b42a; box-shadow: 0 0 0 3px color-mix(in srgb, #00b42a 16%, transparent); }
.device-card .device-fields { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; max-width: none; margin: 0; padding: 0; }
.device-card .device-fields label { min-height: 76px; box-sizing: border-box; justify-content: center; gap: 8px; padding: 14px 16px; border: 1px solid var(--line); border-radius: 12px; background: var(--surface-2); }
.device-field-label { display: flex; align-items: center; gap: 8px; color: var(--muted); font-size: 12px; }
.device-field-icon { display: inline-flex; align-items: center; justify-content: center; width: 22px; height: 22px; border-radius: 7px; background: var(--surface-4); color: var(--accent); font-size: 11px; font-style: normal; font-weight: 700; }
.device-card .device-fields strong { padding-left: 30px; color: var(--text); font-size: 13px; font-weight: 600; }
.device-card .device-fields strong.mono { font-size: 11px; line-height: 1.45; }
.device-card-foot { padding-top: 2px; color: var(--muted); font-size: 11px; }

/* Keep every settings page inside the visible window at narrow sizes. */
.settings-shell,
.settings-panel,
.settings-content,
.settings-content > .setting-card { min-width: 0; max-width: 100%; overflow-x: hidden; }
.settings-content > .setting-card { box-sizing: border-box; }
.profile-card { min-width: 0; }
.profile-edit { min-width: 0; }
.profile-edit :deep(.arco-input-wrapper) { max-width: 100%; }
.setting-line { min-width: 0; flex-wrap: wrap; padding-top: 8px; padding-bottom: 8px; }
.setting-line > div { min-width: 0; flex: 1 1 240px; }
.setting-line > code,
.setting-line > :deep(.arco-btn),
.setting-line > :deep(.arco-select-view) { flex: 0 0 auto; max-width: 100%; }
.network-summary { min-width: 0; flex-wrap: wrap; }
.network-summary > div:nth-child(2) { min-width: 0; flex: 1 1 240px; }
.network-summary > :deep(.arco-btn) { flex: 0 0 auto; }
.path,
.mono,
.info-fields strong,
.basic-info strong,
.device-fields strong { min-width: 0; max-width: 100%; overflow-wrap: anywhere; word-break: break-word; }
.device-card .device-fields { min-width: 0; width: 100%; }
.about-card { box-sizing: border-box; }
.about-card h2.about-chinese-name { margin: 0 0 3px; color: var(--text); font-size: 25px; line-height: 1.25; font-weight: 700; }
.about-card .about-english-name { margin: 0 0 8px; color: var(--muted); font-size: 13px; line-height: 1.4; }
.about-link-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 12px 0; border-bottom: 1px solid var(--line); color: var(--muted); }
.about-link-row > span { padding: 0 !important; border-bottom: 0 !important; }
.about-rows > span { display: grid; grid-template-columns: 72px minmax(0, 1fr); align-items: start; gap: 16px; }
.about-rows > span > strong { min-width: 0; text-align: right; overflow-wrap: anywhere; word-break: break-word; }
.repo-link { display: inline-flex; align-items: center; gap: 6px; min-width: 0; padding: 0; border: 0; background: transparent; color: var(--accent); cursor: pointer; font: inherit; text-align: right; }
.repo-link:hover { text-decoration: underline; }
.repo-link svg { width: 14px; height: 14px; flex: 0 0 14px; }

@media (max-width: 1050px) {
  .settings-content { width: calc(100% - 32px); }
  .setting-card { padding-left: 22px; padding-right: 22px; }
  .profile-card { gap: 18px; }
}

/* Selected navigation follows the current neutral surface instead of using a blue fill. */
.rail-nav button.active,
.rail-settings.active { box-sizing: border-box; color: var(--text); background: var(--surface-4); border-left: 3px solid var(--accent); box-shadow: none; }
.rail-nav button.active:hover,
.rail-settings.active:hover { background: var(--surface-4); color: var(--text); }
.chat-app.theme-dark .rail-nav button.active,
.chat-app.theme-dark .rail-settings.active { background: var(--surface-4); color: var(--text); }
.settings-tabs button.active { color: var(--accent); border-color: var(--accent); }

/* Friends and discovery right panes use the same content surface as settings. */
.chat-app .conversation,
.chat-app .message-scroll,
.chat-app .blank-state,
.chat-app .detail-pane { background: var(--surface-1); }
.chat-app .conversation-head,
.chat-app .composer,
.chat-app .info-pane { background: var(--surface-1); }
.chat-app .message-scroll {
  scrollbar-width: thin;
  scrollbar-color: var(--scroll-thumb) var(--scroll-track);
}
.chat-app .message-scroll::-webkit-scrollbar { width: 10px; }
.chat-app .message-scroll::-webkit-scrollbar-track { background: var(--scroll-track); }
.chat-app .message-scroll::-webkit-scrollbar-thumb { background: var(--scroll-thumb); border: 2px solid var(--scroll-track); border-radius: 999px; }
.chat-app .message-scroll::-webkit-scrollbar-thumb:hover { background: var(--accent); }
.chat-app.theme-dark .list-pane,
.chat-app.theme-dark .discovery-pane { background: var(--list-bg); }
.chat-app.theme-dark .message-scroll,
.chat-app.theme-dark .detail-pane { background: var(--surface-1); }
.chat-app.theme-dark .list-pane .peer-row:hover,
.chat-app.theme-dark .list-pane .peer-row.selected,
.chat-app.theme-dark .discovery-pane .request-row:hover,
.chat-app.theme-dark .discovery-pane .request-row.selected { background: #2b3035; }
.chat-app:not(.theme-dark) .list-pane .peer-row:hover,
.chat-app:not(.theme-dark) .list-pane .peer-row.selected,
.chat-app:not(.theme-dark) .discovery-pane .request-row:hover,
.chat-app:not(.theme-dark) .discovery-pane .request-row.selected { background: #e8e5e1; }

/* macOS-only frameless chrome. Windows keeps its native framed titlebar and the
   right-side layout does not receive any extra titlebar padding. */
:global(html),
:global(body),
:global(#app) { background: var(--window-corner-bg, #edf0f3); }
:global(body.flyqpro-dark),
:global(body.flyqpro-dark #app) { background: var(--window-corner-bg, #0f1115); }
.chat-app { position: relative; border-radius: 0; overflow: hidden; }
.chat-app.is-mac { border-radius: 16px; }
/* Keep the native Windows title bar, while rounding only the lower content
   corners. The rule is independent of the theme so light and dark windows
   have the same silhouette. */
.chat-app.is-windows { border-radius: 0 0 12px 12px; }
.window-drag-region {
  position: fixed;
  z-index: 20;
  top: 0;
  left: 0;
  right: 0;
  height: 38px;
  pointer-events: auto;
  cursor: grab;
  user-select: none;
  -webkit-app-region: drag;
  --wails-draggable: drag;
  --wails-non-client-region: caption;
  background: transparent;
}
/* The macOS drag strip overlaps the top of the workspace. Keep the discovery
   header above it so the scan control always receives pointer events. */
.chat-app.is-mac .discovery-pane > .pane-title {
  position: relative;
  z-index: 21;
}
.chat-app.is-mac .discovery-pane > .pane-title .scan-button {
  position: relative;
  z-index: 22;
  pointer-events: auto;
}
.chat-app.is-mac .friend-pane-title {
  position: relative;
  z-index: 21;
  pointer-events: none;
}
.chat-app.is-mac .friend-pane-title .friend-search,
.chat-app.is-mac .friend-pane-title .icon-button {
  position: relative;
  z-index: 22;
  pointer-events: auto;
}
.chat-app.is-mac .conversation-head {
  position: relative;
  z-index: 21;
  pointer-events: none;
}
.chat-app.is-mac .conversation-head button {
  position: relative;
  z-index: 22;
  pointer-events: auto;
}
.request-times {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
  margin-top: 12px;
  color: var(--muted-text);
  font-size: 12px;
  text-align: left;
}
.request-times span {
  display: flex;
  justify-content: space-between;
  gap: 16px;
}
.request-times strong {
  color: var(--text-color);
  font-weight: 500;
  text-align: right;
}
.detail-primary-button {
  margin-top: 24px;
  border-radius: 14px !important;
}
.mac-window-controls {
  position: fixed;
  z-index: 30;
  top: 10px;
  left: 8px;
  width: 48px;
  height: 16px;
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.mac-control {
  width: 12px;
  height: 12px;
  flex: 0 0 12px;
  padding: 0;
  border: 0;
  border-radius: 50%;
  cursor: pointer;
  box-shadow: inset 0 0 0 0.5px rgba(0, 0, 0, .18), 0 1px 2px rgba(0, 0, 0, .12);
  position: relative;
}
.mac-control.close { background: #ff5f57; }
.mac-control.minimise { background: #febc2e; }
.mac-control.maximise { background: #28c840; }
.mac-control::before {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: rgba(58, 38, 20, .78);
  font-family: Arial, sans-serif;
  font-size: 9px;
  font-weight: 700;
  line-height: 12px;
  opacity: 1;
}
.mac-control.close::before { content: '×'; color: rgba(75, 18, 15, .82); }
.mac-control.minimise::before { content: '−'; color: rgba(83, 54, 7, .86); }
.mac-control.maximise::before { content: '＋'; color: rgba(12, 72, 25, .82); font-size: 8px; }
.mac-control:hover { filter: brightness(.9); }
.mac-control:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.chat-app.is-mac .rail {
  position: relative;
  z-index: 1;
  padding-top: 48px;
}
.workspace > .list-pane,
.workspace > .conversation,
.workspace > .detail-pane,
.workspace > .blank-state,
.workspace > .info-pane { min-height: 0; }

/* Chat interaction additions. */
.conversation { position: relative; }
.conversation-head { height: 58px; flex-basis: 58px; padding: 0 18px; }
.head-peer strong { font-size: 14px; }
.head-peer span { font-size: 11px; }
.message-line { align-items: center; gap: 8px; }
.message-avatar { width: 32px; height: 32px; border-radius: 10px; font-size: 12px; }
.message-status { display: inline-flex; width: 52px; height: 17px; margin-left: 6px; align-items: center; justify-content: center; border-radius: 4px; background: rgba(255, 255, 255, .2); font-size: 10px; vertical-align: middle; }
.chat-app .message-line.mine .message-status { color: var(--message-outgoing-text); background: rgba(255, 255, 255, .18); }
.message-status.rejected { width: auto; min-width: 52px; padding: 0 6px; color: #d4380d; background: #fff1f0; font-weight: 600; }
.chat-app.theme-dark .message-status.rejected { color: #ffb4ab; background: #4a2525; }
.message-bubble.attachment-bubble { width: min(340px, calc(100vw - 64px)); max-width: min(340px, calc(100vw - 64px)); box-sizing: border-box; }
.message-bubble.attachment-bubble.image-attachment-bubble { width: fit-content; max-width: min(300px, calc(100vw - 64px)); }
.transfer-progress { width: 100%; height: 66px; min-height: 66px; margin-top: 8px; padding-top: 7px; box-sizing: border-box; border-top: 1px solid color-mix(in srgb, currentColor 14%, transparent); font-variant-numeric: tabular-nums; }
.transfer-progress-head { display: grid; grid-template-columns: minmax(0, 1fr) 40px 48px; align-items: center; gap: 7px; height: 20px; font-size: 11px; opacity: .82; }
.transfer-progress-speed { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.transfer-details-button { min-width: 38px; padding: 0; border: 0; background: transparent; color: inherit; font-size: 11px; cursor: pointer; opacity: .82; }
.transfer-progress-head :deep(.arco-btn) { width: 48px; justify-content: center; padding: 0; }
.transfer-progress-track { height: 5px; margin-top: 5px; overflow: hidden; border-radius: 999px; background: color-mix(in srgb, currentColor 14%, transparent); }
.transfer-progress-track i { display: block; height: 100%; border-radius: inherit; background: var(--accent); transition: width .18s ease; }
.transfer-progress-foot { display: flex; align-items: center; justify-content: space-between; gap: 8px; height: 17px; margin-top: 4px; color: color-mix(in srgb, currentColor 72%, transparent); font-size: 10px; line-height: 17px; white-space: nowrap; }
.transfer-progress-foot span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.vertical-resizer { width: 5px; flex: 0 0 5px; margin-left: -3px; margin-right: -2px; cursor: col-resize; position: relative; z-index: 6; }
.vertical-resizer:hover::after, .vertical-resizer:active::after { content: ''; position: absolute; inset: 0 1px; background: var(--accent); }
.horizontal-resizer { height: 5px; flex: 0 0 5px; margin-top: -3px; cursor: row-resize; position: relative; z-index: 5; }
.horizontal-resizer:hover::after, .horizontal-resizer:active::after { content: ''; position: absolute; inset: 1px 0; background: var(--accent); }
.composer { box-sizing: border-box; flex: 0 0 auto; min-height: 120px; position: relative; overflow: auto; transition: height .18s ease; }
.emoji-panel { display: flex; flex-wrap: wrap; gap: 4px; padding: 7px 0; }
.emoji-panel button { width: 28px; height: 28px; border: 0; background: transparent; cursor: pointer; font-size: 18px; }
.pending-images { display: flex; flex-wrap: wrap; gap: 8px; padding: 6px 0; }
.pending-files { display: flex; flex-wrap: wrap; gap: 6px; max-height: 54px; overflow: auto; padding: 4px 0; }
.pending-file { display: inline-flex; align-items: center; gap: 5px; max-width: 260px; padding: 5px 8px; border: 1px solid var(--line); border-radius: 7px; background: var(--surface-2); color: var(--text); font-size: 12px; }
.pending-file span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pending-file small { color: var(--muted); white-space: nowrap; }
.pending-file button { display: inline-flex; border: 0; background: transparent; color: var(--muted); cursor: pointer; padding: 0; }
.pending-image { width: 58px; height: 58px; position: relative; border-radius: 6px; overflow: hidden; border: 1px solid var(--line); }
.pending-image img { width: 100%; height: 100%; object-fit: cover; }
.pending-image button { position: absolute; top: 2px; right: 2px; display: flex; border: 0; border-radius: 50%; padding: 2px; background: rgba(0, 0, 0, .64); color: #fff; cursor: pointer; }
.image-message { position: relative; display: block; width: fit-content; max-width: min(270px, calc(100vw - 64px)); min-width: 0; min-height: 110px; padding: 0; border: 0; background: var(--surface-3); cursor: zoom-in; overflow: hidden; border-radius: 8px; color: inherit; }
.image-message.is-transferring { cursor: progress; }
.image-message:disabled { opacity: 1; }
.image-pending-placeholder { display: flex; min-height: 110px; align-items: center; justify-content: center; padding: 0 18px; color: var(--muted); font-size: 12px; }
.image-message img { display: block; width: auto; max-width: 100%; height: auto; max-height: 220px; object-fit: contain; margin: 0 auto; }
.image-transfer-mask { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px; background: rgba(9, 14, 24, .58); color: #fff; font-size: 12px; letter-spacing: .02em; pointer-events: auto; }
.image-transfer-mask :deep(.image-transfer-cancel) { min-width: 58px; border: 1px solid rgba(255, 255, 255, .72); border-radius: 6px; background: #e5484d !important; color: #fff !important; box-shadow: 0 2px 8px rgba(0, 0, 0, .32); font-weight: 600; }
.image-transfer-mask :deep(.image-transfer-cancel:hover) { background: #f06a6f !important; }
.image-progress-ring { position: relative; --progress: 0%; display: inline-flex; width: 62px; height: 62px; align-items: center; justify-content: center; border-radius: 50%; background: conic-gradient(var(--accent) var(--progress), rgba(255, 255, 255, .28) var(--progress)); box-shadow: 0 4px 18px rgba(0, 0, 0, .22); }
.image-progress-ring::after { content: ''; position: absolute; width: 50px; height: 50px; border-radius: 50%; background: rgba(13, 20, 34, .9); }
.image-progress-ring strong { position: relative; z-index: 1; font-size: 13px; font-weight: 700; }
.new-message-button { position: absolute; right: 22px; top: -41px; z-index: 8; border: 0; border-radius: 5px; padding: 7px 10px; background: var(--accent); color: #fff; font-size: 12px; cursor: pointer; box-shadow: var(--shadow); }
.info-overlay { position: absolute; z-index: 12; top: 52px; right: 12px; bottom: 12px; width: min(320px, calc(100% - 24px)); overflow: auto; box-sizing: border-box; border: 1px solid var(--line); border-radius: 8px; box-shadow: var(--shadow); display: flex; flex-direction: column; }
.info-fields input { width: 100%; box-sizing: border-box; padding: 7px 8px; border: 1px solid var(--line); border-radius: 4px; background: var(--surface-2); color: var(--text); }
.pane-title { height: 52px; flex: 0 0 52px; box-sizing: border-box; padding: 0 16px; border-bottom: 1px solid var(--line); background: var(--surface-2); }
.discovery-pane > .pane-title { justify-content: flex-end; }
.friend-pane-title > div { flex: 0 0 auto; }
.friend-search { flex: 1 1 auto; min-width: 0; margin: 0 10px 0 12px; }
.friend-search :deep(.arco-input-wrapper) { height: 30px; box-sizing: border-box; border: 1px solid var(--line); border-radius: 8px; background: var(--surface-1); box-shadow: 0 1px 3px rgba(37, 48, 62, .06); }
.friend-search :deep(.arco-input-wrapper:focus-within) { border-color: var(--accent); box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 18%, transparent); }
.friend-search :deep(.arco-input-prefix) { color: var(--muted); }
.search { margin-top: 12px; margin-bottom: 12px; }
.group-title { align-items: center; }
.group-title > span { display: inline-flex; align-items: center; gap: 6px; min-height: 22px; line-height: 20px; }
.group-title > span svg { width: 14px; height: 14px; flex: 0 0 14px; }
.group-title-row { display: flex; align-items: center; }
.group-title-row .group-title { flex: 1; min-width: 0; }
.clear-requests { flex: 0 0 auto; margin: 8px 16px 0 0; border: 0; background: transparent; color: #86909c; font-size: 12px; cursor: pointer; padding: 3px 4px; }
.clear-requests:hover { color: #165dff; }

/* Chat density and interaction polish. */
.conversation-head { height: 52px; flex-basis: 52px; padding: 0 18px; }
.head-peer { min-width: 0; gap: 9px; }
.head-peer strong { max-width: min(45vw, 360px); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.head-peer > .head-status { display: inline-flex; align-items: center; gap: 5px; min-width: 0; color: var(--muted); white-space: nowrap; }
.head-status i { width: 7px; height: 7px; flex: 0 0 7px; border-radius: 50%; background: var(--muted); }
.head-status i.online { background: #00b42a; }
.message-scroll { padding: 18px 14px; }
.message-line { margin: 8px 0; gap: 10px; }
.message-retry { width: 22px; height: 22px; flex: 0 0 22px; border: 0; border-radius: 50%; background: #f53f3f; color: #fff; font-weight: 800; line-height: 22px; cursor: pointer; box-shadow: 0 3px 8px rgba(245, 63, 63, .25); }
.message-retry:hover { background: #cb2634; }
.message-retry:disabled { opacity: .55; cursor: wait; }
.message-line { animation: message-enter .18s cubic-bezier(.22, .8, .28, 1) both; content-visibility: auto; contain-intrinsic-size: 52px; }
.message-bubble { max-width: min(72%, 680px); padding: 9px 12px; border-radius: 14px 14px 14px 5px; line-height: 1.45; }
.message-line.mine .message-bubble { border-radius: 14px 14px 5px 14px; }
.message-bubble.text-bubble { position: relative; }
.message-bubble.text-bubble::after { content: ''; position: absolute; top: 50%; width: 14px; height: 18px; transform: translateY(-50%); background: inherit; pointer-events: none; }
.message-line:not(.mine) .message-bubble.text-bubble::after { left: -7px; clip-path: polygon(100% 0, 100% 100%, 0 50%); border-radius: 3px 0 0 3px; }
.message-line.mine .message-bubble.text-bubble::after { right: -7px; clip-path: polygon(0 0, 100% 50%, 0 100%); border-radius: 0 3px 3px 0; }
.message-avatar { width: 32px; height: 32px; border-radius: 10px; }
.avatar-button { padding: 0; border: 0; font: inherit; cursor: pointer; transition: transform .16s ease, filter .16s ease; }
.avatar-button:hover { transform: translateY(-1px); filter: brightness(1.06); }
.peer-row { position: relative; padding-right: 42px; }
.peer-copy { flex: 1; }
.peer-device { color: #a0a8b3 !important; font-size: 11px !important; }
.unread-badge { position: absolute; right: 10px; top: 50%; min-width: 18px; height: 18px; padding: 0 5px; border-radius: 10px; transform: translateY(-50%); background: #f53f3f; color: #fff !important; font-size: 10px !important; font-weight: 700 !important; line-height: 18px; text-align: center; }
.peer-row { gap: 10px; padding-right: 20px; }
.peer-row > .avatar { width: 38px; height: 38px; border-radius: 12px; font-size: 14px; }
.peer-meta { flex: 0 0 auto; min-width: 42px; display: flex; flex-direction: column; align-items: flex-end; gap: 5px; }
.peer-time { color: var(--muted); font-size: 11px; line-height: 18px; white-space: nowrap; }
.peer-meta .unread-badge { position: static; transform: none; }
.rail-unread-badge { right: 2px !important; top: 0 !important; }
.info-overlay { position: fixed; z-index: 40; top: 52px; right: 0; bottom: 0; width: min(320px, 100vw); padding: 16px; border-radius: 14px 0 0 14px; display: flex; flex-direction: column; }
.peer-info-enter-active,
.peer-info-leave-active { transition: transform .28s cubic-bezier(.22, .8, .28, 1), opacity .22s ease; will-change: transform, opacity; }
.peer-info-enter-from,
.peer-info-leave-to { transform: translateX(100%); opacity: 0; }
.peer-info-enter-to,
.peer-info-leave-from { transform: translateX(0); opacity: 1; }
.info-profile { padding: 20px 0 18px; }
.composer { display: flex; flex-direction: column; gap: 2px; padding: 8px 18px 10px; overflow: visible; }
.composer-tools { height: 24px; flex: 0 0 24px; }
.composer-editor { min-height: 0; flex: 1 1 auto; display: flex; flex-direction: column; overflow: hidden; }
.composer textarea { flex: 1 1 auto; min-height: 36px; overflow: auto; padding: 5px 0; }
.composer-foot { min-height: 28px; flex: 0 0 28px; gap: 12px; }
.composer-foot > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.composer-foot :deep(.arco-btn) { flex: 0 0 auto; min-width: 72px; }
.emoji-panel { position: absolute; left: 18px; bottom: calc(100% - 2px); z-index: 15; box-sizing: border-box; display: grid; grid-template-columns: repeat(8, 1fr); align-content: start; gap: 4px; width: 360px; height: 272px; max-width: calc(100vw - 36px); padding: 10px; overflow-y: auto; overscroll-behavior: contain; border: 1px solid var(--line); border-radius: 10px; background: var(--surface-1); box-shadow: var(--shadow); opacity: 0; visibility: hidden; pointer-events: none; transform: translateY(18px); will-change: transform, opacity; transition: opacity .15s cubic-bezier(.22, .8, .28, 1), transform .15s cubic-bezier(.22, .8, .28, 1), visibility 0s linear .15s; }
.emoji-panel.is-open { opacity: 1; visibility: visible; pointer-events: auto; transform: translateY(0); transition-delay: 0s; }
.emoji-panel button { display: flex; width: 36px; height: 36px; align-items: center; justify-content: center; border-radius: 7px; font-size: 22px; line-height: 1; }
.emoji-panel button:hover { background: var(--hover); }
.pending-files { flex: 0 0 auto; box-sizing: border-box; max-height: 84px; overflow-y: auto; overflow-x: hidden; flex-wrap: wrap; align-content: flex-start; padding: 3px 0 6px; transition: height .18s ease; }
.pending-file { min-width: 0; box-sizing: border-box; }
.pending-images { flex: 0 0 auto; max-height: 50px; overflow-x: auto; flex-wrap: nowrap; padding: 3px 0; }
.pending-image { width: 44px; height: 44px; flex: 0 0 44px; }

/* Discovery keeps its scan control fixed while long groups scroll independently. */
.discovery-pane { min-height: 0; overflow: hidden; }
.discovery-scroll { flex: 1 1 auto; min-height: 0; overflow-x: hidden; overflow-y: auto; padding-bottom: 20px; scrollbar-width: thin; scrollbar-color: var(--scroll-thumb) var(--scroll-track); }
.discovery-scroll::-webkit-scrollbar { width: 8px; }
.discovery-scroll::-webkit-scrollbar-track { background: var(--scroll-track); }
.discovery-scroll::-webkit-scrollbar-thumb { background: var(--scroll-thumb); border-radius: 999px; }

/* Pinned conversations remain visibly distinct without changing the stable sort. */
.chat-app:not(.theme-dark) .list-pane .peer-row.pinned { background: #e9e6e1; }
.chat-app:not(.theme-dark) .list-pane .peer-row.pinned:hover,
.chat-app:not(.theme-dark) .list-pane .peer-row.pinned.selected { background: #ded9d1; }
.chat-app.theme-dark .list-pane .peer-row.pinned { background: #252d39; }
.chat-app.theme-dark .list-pane .peer-row.pinned:hover,
.chat-app.theme-dark .list-pane .peer-row.pinned.selected { background: #303b4c; }
.pin-mark { display: inline-flex; align-items: center; margin-left: 6px; padding: 1px 5px; border-radius: 999px; background: color-mix(in srgb, var(--accent) 16%, transparent); color: var(--accent); font-size: 10px; font-style: normal; font-weight: 600; vertical-align: middle; }

/* Context actions and confirmations are opaque, anchored surfaces so the app remains visible. */
.message-context-menu,
.peer-context-menu,
.contact-context-menu { max-width: min(260px, calc(100vw - 16px)); box-sizing: border-box; background: var(--surface-1); color: var(--text); border: 1px solid var(--line); box-shadow: 0 12px 30px rgba(20, 30, 60, .28); }
.message-context-menu button,
.peer-context-menu button,
.contact-context-menu button { max-width: min(240px, calc(100vw - 24px)); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.contact-context-menu { position: fixed; z-index: 9999; min-width: 150px; padding: 6px; display: flex; flex-direction: column; border-radius: 9px; }
.contact-context-menu button { border: 0; background: transparent; color: inherit; cursor: pointer; text-align: left; font-size: 13px; padding: 8px 10px; border-radius: 6px; }
.contact-context-menu button:hover { background: rgba(55, 103, 232, .12); }
.contact-context-menu button.danger { color: #f53f3f; }
.delete-confirm-popover { position: fixed; z-index: 10000; width: min(310px, calc(100vw - 24px)); box-sizing: border-box; padding: 14px 16px; border: 1px solid var(--line); border-radius: 10px; background: var(--surface-1); color: var(--text); box-shadow: 0 14px 34px rgba(20, 30, 60, .25); }
.delete-confirm-popover strong { display: block; font-size: 13px; }
.delete-confirm-popover p { margin: 9px 0 13px; color: var(--muted); font-size: 12px; line-height: 1.55; }
.delete-confirm-actions { display: flex; justify-content: flex-end; gap: 8px; }
.delete-confirm-actions button { border: 1px solid var(--line); border-radius: 6px; padding: 6px 11px; background: var(--surface-2); color: var(--text); cursor: pointer; font-size: 12px; }
.delete-confirm-actions button:hover { background: var(--hover); }
.delete-confirm-actions button.danger { border-color: color-mix(in srgb, #f53f3f 40%, var(--line)); background: #f53f3f; color: #fff; }
.chat-app.theme-dark .contact-context-menu,
.chat-app.theme-dark .delete-confirm-popover { background: #253249; color: #e5e7eb; border-color: #465875; box-shadow: 0 16px 38px rgba(0, 0, 0, .48); }

@keyframes message-enter {
  from { opacity: 0; transform: translateY(5px) scale(.99); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}

@media (prefers-reduced-motion: reduce) {
  .message-line { animation: none !important; }
}

@media (max-width: 760px) {
  .vertical-resizer { display: none; }
  .list-pane { width: 220px !important; flex-basis: 220px !important; }
  .message-scroll { padding-left: 14px; padding-right: 14px; }
}

.discovery-pane .pending-request { border-left: 3px solid #ff7d00; padding-left: 15px; background: #fff7e8; }
.chat-app .discovery-pane .pending-request,
.chat-app .discovery-pane .pending-request:hover,
.chat-app .discovery-pane .pending-request.selected { background: #fff7e8; border-left-color: #ff7d00; }
.request-avatar { position: relative; }
.discovery-pane .avatar i.request-pending-dot { position: absolute; top: -2px; right: -2px; bottom: auto; width: 9px; height: 9px; border: 2px solid #fff; border-radius: 50%; background: #f53f3f !important; }
.request-status-line { display: flex; align-items: center; min-width: 0; gap: 6px; overflow: visible !important; white-space: normal !important; }
.request-status-text { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pending-request-mark { flex: 0 0 auto; color: #d25f00; background: #fff0d4; border-radius: 10px; padding: 2px 7px; font-size: 11px; font-style: normal; font-weight: 600; line-height: 1.2; }
.chat-app.theme-dark .pending-request { background: #392c1e; border-left-color: #ffb65c; }
.chat-app.theme-dark .discovery-pane .pending-request,
.chat-app.theme-dark .discovery-pane .pending-request:hover,
.chat-app.theme-dark .discovery-pane .pending-request.selected { background: #392c1e; border-left-color: #ffb65c; }
.chat-app.theme-dark .pending-request-mark { color: #ffd591; background: #513719; }
.chat-app.theme-dark .request-pending-dot { border-color: #253249; }

/* Keep user-controlled names from pushing status text and action controls
   out of compact rows. The service limits newly saved names to ten Unicode
   characters; this also protects displays containing legacy remote data. */
.nickname-ellipsis {
  display: block;
  min-width: 0;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.nickname-ellipsis-inline {
  display: inline-block;
  max-width: 14em;
  overflow: hidden;
  text-overflow: ellipsis;
  vertical-align: bottom;
  white-space: nowrap;
}
.peer-copy,
.request-row > div:not(.avatar),
.head-peer,
.info-profile,
.self-profile-name,
.detail-card { min-width: 0; }
.peer-copy > strong { display: flex; min-width: 0; align-items: center; }
.peer-copy > strong > .nickname-ellipsis { flex: 1 1 auto; }
.head-peer > .nickname-ellipsis { flex: 0 1 auto; }
.forward-targets :deep(.arco-checkbox-label) { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.delete-confirm-popover > strong { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.detail-card > .nickname-ellipsis,
.info-profile > .nickname-ellipsis { width: 100%; text-align: center; }

/* Shared list primitives: keep high-frequency rows aligned across friends,
   contacts, requests and the discovery view while leaving their business
   actions and platform-specific layout intact. */
.chat-app .peer-row,
.chat-app .request-row {
  min-height: var(--fp-row-height);
  box-sizing: border-box;
}
.chat-app .peer-row > .avatar,
.chat-app .request-row > .avatar {
  width: var(--fp-avatar-lg);
  height: var(--fp-avatar-lg);
  flex: 0 0 var(--fp-avatar-lg);
  border-radius: var(--fp-radius-lg);
}
.chat-app .peer-row,
.chat-app .request-row { gap: var(--fp-space-3); }
.chat-app .request-row > div:not(.avatar),
.chat-app .peer-row .peer-copy { min-width: 0; }
.chat-app .request-row strong,
.chat-app .peer-row strong,
.chat-app .detail-card h2,
.chat-app .head-peer strong {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chat-app .pane-title,
.chat-app .conversation-head,
.chat-app .settings-head {
  background: var(--surface-2);
  border-color: var(--line);
}
.chat-app .icon-button,
.chat-app .scan-button,
.chat-app .composer-tools button {
  min-width: var(--fp-control-sm);
  min-height: var(--fp-control-sm);
  border-radius: var(--fp-radius-md);
}
.chat-app .group-title,
.chat-app .clear-requests { color: var(--muted); }
.chat-app .group-title:hover,
.chat-app .clear-requests:hover { color: var(--accent); }
.chat-app .message-status,
.chat-app .pending-request-mark,
.chat-app .pin-mark { border-radius: 999px; }
.chat-app .message-context-menu,
.chat-app .peer-context-menu,
.chat-app .contact-context-menu,
.chat-app .delete-confirm-popover {
  border-radius: var(--fp-radius-md);
  background: var(--surface-1);
  color: var(--text);
}
.chat-app:not(.theme-dark) .message-context-menu,
.chat-app:not(.theme-dark) .peer-context-menu,
.chat-app:not(.theme-dark) .contact-context-menu,
.chat-app:not(.theme-dark) .delete-confirm-popover {
  box-shadow: 0 12px 30px rgba(20, 30, 60, .18);
}
.chat-app.theme-dark .message-context-menu,
.chat-app.theme-dark .peer-context-menu,
.chat-app.theme-dark .contact-context-menu,
.chat-app.theme-dark .delete-confirm-popover {
  box-shadow: 0 16px 38px rgba(0, 0, 0, .42);
}
</style>
