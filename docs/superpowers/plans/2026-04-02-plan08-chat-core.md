# Plan 8: 客户端聊天核心 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 完善聊天体验：消息渲染优化、聊天头部、回复消息、输入状态、滚动行为、DM创建、频道列表排序

**Architecture:** 纯客户端改进为主，少量服务端支持（typing indicator via WebSocket）。

**Tech Stack:** Angular, TypeScript, SCSS, WebSocket

---

## 目录结构（Plan 8 新增/修改文件）

```
client/src/app/
├── core/
│   ├── channels/
│   │   └── channel.service.ts        # MODIFY: sortedChannels computed, markRead on enter, DM name resolution
│   └── messages/
│       └── message.service.ts        # MODIFY: replyTo state signal, markRead call on loadMessages
├── features/
│   ├── channel-list/
│   │   ├── channel-list.component.ts   # MODIFY: search filter, sorted list, online dot
│   │   ├── channel-list.component.html # MODIFY: search input, DM peer name, online dot
│   │   └── channel-list.component.scss # MODIFY: search bar styles, online dot
│   ├── chat/
│   │   ├── chat.component.ts           # MODIFY: header data, reply state, scroll sentinel, mark-read
│   │   ├── chat.component.html         # MODIFY: header, message groups, reply preview, jump-to-bottom
│   │   └── chat.component.scss         # MODIFY: header, reply preview, jump button, system messages
│   └── contacts/
│       ├── contacts.component.ts       # MODIFY: openDM() method
│       └── contacts.component.html     # MODIFY: "Message" button on friends tab
```

---

## Task 1: Chat Header Component

**Goal:** Add a header bar above the message list showing channel name (or DM peer display name), member count for groups, and a settings gear icon that links to `/channels/:id/settings`.

### 1.1 Modify `channel.service.ts` — expose DM peer name helper

The channel list already has `ChannelWithPreview`. For DM channels (`type === 1`), the `name` field is empty on the server. We need to resolve the peer's display name from the friends list.

**File:** `client/src/app/core/channels/channel.service.ts`

Add a new method `getDMPeerName(channel: ChannelWithPreview): string`:

```typescript
// Add import at the top
import { FriendService } from '../friends/friend.service';

// Inside ChannelService class — inject FriendService
private friendService = inject(FriendService);

/**
 * For a DM channel (type === 1), return the peer's display_name
 * looked up from the friends list. Falls back to "DM" if not found.
 */
getDMPeerName(channelId: number): string {
  // DM channel names are encoded as "dm:{peerId}" on the server
  // OR we scan the friends list for the matching channel.
  // Since we don't store peer_id on ChannelWithPreview, use the channel name
  // field which the server sets to "" for DMs — fall back to friends list scan.
  // NOTE: For now we return 'DM' — Task 4 (DM creation) will set proper names
  // by storing peer_id in the channel object returned from POST /api/channels/dm.
  return 'DM';
}
```

> **Note:** Full DM name resolution (mapping channel → peer user) requires the server to return `peer_id` on DM channels or a separate `/api/channels/:id/dm-peer` call. For Plan 8 we do a best-effort lookup: check if `channel.name` starts with `dm:` (set by Task 4), otherwise scan the friends signal. The header and channel list use the same helper.

**Add computed helper in `ChannelService`:**

```typescript
/**
 * Returns a human-readable label for any channel.
 * - GROUP (type 2): uses channel.name
 * - DM (type 1): scans friends list for matching display_name stored in channel.name
 *   (populated by Task 4 when creating/navigating DMs)
 */
channelLabel(ch: ChannelWithPreview): string {
  if (ch.type === 2) return ch.name || 'Group';
  // DM: channel.name is set to peer display_name by the create-DM flow (Task 4).
  // If it's blank (legacy channels), fall back to "DM".
  return ch.name || 'DM';
}
```

### 1.2 Add member count to `ChannelService`

Add a simple in-memory cache for member counts:

```typescript
/** Cached member counts keyed by channel id — loaded on demand by ChatComponent */
readonly memberCounts = signal<Record<number, number>>({});

async loadMemberCount(channelId: number): Promise<void> {
  try {
    const members = await this.listMembers(channelId);
    this.memberCounts.update(counts => ({ ...counts, [channelId]: members.length }));
  } catch {
    // non-fatal: header shows nothing if count fails
  }
}
```

### 1.3 Modify `chat.component.ts` — load channel info for header

**File:** `client/src/app/features/chat/chat.component.ts`

Add imports and state:

```typescript
import { ChannelService, ChannelWithPreview } from '../../core/channels/channel.service';
import { Router } from '@angular/router';

// Inside class:
private channelService = inject(ChannelService);
private router = inject(Router);

/** The ChannelWithPreview for the currently-open channel. */
readonly activeChannel = computed<ChannelWithPreview | undefined>(() =>
  this.channelService.channels().find(ch => ch.id === this.channelId)
);

readonly channelName = computed(() => {
  const ch = this.activeChannel();
  if (!ch) return '';
  return this.channelService.channelLabel(ch);
});

readonly memberCount = computed(() =>
  this.channelService.memberCounts()[this.channelId] ?? null
);
```

In `loadChannel()`, after loading messages, also load the member count:

```typescript
private async loadChannel(channelId: number): Promise<void> {
  this.error.set(null);
  this.replyTarget.set(null);
  try {
    await Promise.all([
      this.messageService.loadMessages(channelId),
      this.channelService.loadMemberCount(channelId),
    ]);
    this.shouldScrollToBottom = true;
  } catch (err) {
    this.error.set('Failed to load messages.');
    console.error(err);
  }
}

openSettings(): void {
  this.router.navigate(['channels', this.channelId, 'settings']);
}
```

### 1.4 Add header to `chat.component.html`

Insert above the `.message-list` div:

```html
<!-- Chat header -->
<div class="chat-header">
  <div class="chat-header-info">
    <span class="chat-header-name">{{ channelName() }}</span>
    @if (memberCount() !== null && activeChannel()?.type === 2) {
      <span class="chat-header-meta">{{ memberCount() }} members</span>
    }
  </div>
  <button class="chat-header-settings" title="Settings" (click)="openSettings()">⚙</button>
</div>
```

### 1.5 Add header styles to `chat.component.scss`

```scss
.chat-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.6rem 1rem;
  border-bottom: 1px solid #e5e7eb;
  background: #fff;
  min-height: 52px;
  flex-shrink: 0;
}

.chat-header-info {
  display: flex;
  flex-direction: column;
  gap: 0.1rem;
}

.chat-header-name {
  font-weight: 600;
  font-size: 1rem;
  color: #111;
}

.chat-header-meta {
  font-size: 0.75rem;
  color: #6b7280;
}

.chat-header-settings {
  background: none;
  border: none;
  font-size: 1.2rem;
  cursor: pointer;
  color: #9ca3af;
  padding: 0.25rem;
  border-radius: 4px;
  line-height: 1;

  &:hover {
    color: #374151;
    background: #f3f4f6;
  }
}
```

### Verification

- [ ] Open a group channel: header shows channel name, member count, settings gear
- [ ] Click gear: navigates to `/channels/:id/settings`
- [ ] Open a DM channel: header shows "DM" (or peer name if Task 4 is done)
- [ ] No console errors

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/features/chat/ client/src/app/core/channels/channel.service.ts
git commit -m "feat(chat): add chat header with channel name, member count, settings link"
```

---

## Task 2: Message Rendering — Timestamps, Sender Grouping, System Messages

**Goal:**
1. Group consecutive messages from the same sender (suppress repeated avatar/name for run of messages within 5 min)
2. Show date separators when the day changes
3. Render system messages (`msg_type === 4`) as centered italic lines
4. Show sender display name above first message in a group (for non-mine messages)

### 2.1 Add sender name lookup to `MessageService`

**File:** `client/src/app/core/messages/message.service.ts`

Add import and inject FriendService + AuthService:

```typescript
import { FriendService } from '../friends/friend.service';

private friendService = inject(FriendService);
private authService = inject(AuthService);  // already available via chat.component, add here too

/** Get display name for a sender_id. Returns "You" for self, looks up friends list. */
getSenderName(senderId: number): string {
  const me = this.authService.currentUser();
  if (me && senderId === me.id) return 'You';
  const friend = this.friendService.friends().find(f => f.id === senderId);
  return friend?.display_name ?? `User ${senderId}`;
}
```

### 2.2 Add message grouping computed to `chat.component.ts`

**File:** `client/src/app/features/chat/chat.component.ts`

Add a `GroupedMessage` interface and a `groupedMessages` computed signal:

```typescript
export interface MessageGroup {
  senderId: number;
  senderName: string;
  isMine: boolean;
  messages: Message[];
  /** ISO date string — only set when a date separator should appear above this group */
  dateSeparator: string | null;
  isSystem: boolean;  // true when all messages in group are msg_type === 4
}
```

```typescript
readonly groupedMessages = computed<MessageGroup[]>(() => {
  const msgs = this.visibleMessages();
  const me = this.auth.currentUser();
  const groups: MessageGroup[] = [];
  let lastDate = '';

  for (const msg of msgs) {
    const msgDate = new Date(msg.created_at).toLocaleDateString();
    const dateSeparator = msgDate !== lastDate ? msgDate : null;
    if (dateSeparator) lastDate = msgDate;

    const isSystem = msg.msg_type === 4;
    const isMine = msg.sender_id === (me?.id ?? -1);
    const senderName = this.messageService.getSenderName(msg.sender_id);

    // Merge into last group if same sender, not system, within 5 minutes, no date separator
    const last = groups[groups.length - 1];
    const canMerge =
      last &&
      !isSystem &&
      !last.isSystem &&
      last.senderId === msg.sender_id &&
      dateSeparator === null &&
      new Date(msg.created_at).getTime() -
        new Date(last.messages[last.messages.length - 1].created_at).getTime() <
        5 * 60 * 1000;

    if (canMerge) {
      last.messages.push(msg);
    } else {
      groups.push({
        senderId: msg.sender_id,
        senderName,
        isMine,
        messages: [msg],
        dateSeparator,
        isSystem,
      });
    }
  }

  return groups;
});
```

### 2.3 Update `chat.component.html` — render groups

Replace the `@for (msg of visibleMessages()...)` block with:

```html
@for (group of groupedMessages(); track group.messages[0].id) {
  <!-- Date separator -->
  @if (group.dateSeparator) {
    <div class="date-separator">
      <span>{{ group.dateSeparator }}</span>
    </div>
  }

  <!-- System message -->
  @if (group.isSystem) {
    <div class="system-message">{{ group.messages[0].content }}</div>
  } @else {
    <!-- Normal message group -->
    <div class="message-group" [class.mine]="group.isMine">
      @if (!group.isMine) {
        <div class="group-avatar">{{ group.senderName[0] | uppercase }}</div>
      }
      <div class="group-body">
        @if (!group.isMine) {
          <div class="group-sender">{{ group.senderName }}</div>
        }
        @for (msg of group.messages; track msg.client_msg_id ?? msg.id) {
          <div class="bubble-wrapper" [class.optimistic]="isOptimistic(msg)">
            <!-- Reply preview (Task 6 adds this) -->
            @if (msg.reply_to) {
              <div class="reply-preview">↩ replying to message #{{ msg.reply_to }}</div>
            }
            <div class="bubble" (contextmenu)="onContextMenu($event, msg)">
              <span class="content">{{ msg.content }}</span>
              <span class="timestamp">{{ formatTime(msg.created_at) }}</span>
            </div>
          </div>
        }
      </div>
    </div>
  }
}
```

### 2.4 Update `chat.component.scss` — message groups

Remove the old `.message-row` block and add:

```scss
/* ---- date separator ---- */

.date-separator {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin: 0.75rem 0;
  color: #9ca3af;
  font-size: 0.75rem;

  &::before,
  &::after {
    content: '';
    flex: 1;
    height: 1px;
    background: #e5e7eb;
  }
}

/* ---- system message ---- */

.system-message {
  text-align: center;
  color: #9ca3af;
  font-size: 0.8rem;
  font-style: italic;
  margin: 0.25rem 0;
}

/* ---- message groups ---- */

.message-group {
  display: flex;
  align-items: flex-end;
  gap: 0.5rem;

  &.mine {
    flex-direction: row-reverse;
  }
}

.group-avatar {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: #e5e7eb;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 0.8rem;
  font-weight: 600;
  color: #374151;
  flex-shrink: 0;
  align-self: flex-end;
}

.group-body {
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
  max-width: 60%;
}

.group-sender {
  font-size: 0.75rem;
  color: #6b7280;
  margin-bottom: 0.1rem;
  padding-left: 0.25rem;
}

.bubble-wrapper {
  &.optimistic .bubble {
    opacity: 0.65;
  }
}

/* ---- keep existing .bubble, .content, .timestamp ---- */
/* .mine .bubble gets blue bg via parent .mine selector */

.message-group.mine .bubble {
  background: #007aff;
  color: #fff;
}

/* reply preview stub (Task 6 will enhance) */
.reply-preview {
  font-size: 0.72rem;
  color: #6b7280;
  background: #f3f4f6;
  border-left: 3px solid #9ca3af;
  padding: 0.2rem 0.5rem;
  border-radius: 4px;
  margin-bottom: 0.15rem;
}
```

### 2.5 Add `onContextMenu` stub to `chat.component.ts`

```typescript
/** Placeholder — Task 3 wires up the real context menu */
onContextMenu(event: MouseEvent, msg: Message): void {
  event.preventDefault();
  // Task 3 will implement this
}
```

### Verification

- [ ] Messages from the same sender within 5 min are grouped under one avatar
- [ ] A new sender starts a fresh group with name label
- [ ] Day change shows a centered date separator
- [ ] `msg_type === 4` renders as a centered italic line, no bubble

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/features/chat/ client/src/app/core/messages/message.service.ts
git commit -m "feat(chat): message grouping by sender, date separators, system message rendering"
```

---

## Task 3: Auto Mark-Read on Channel Enter + Unread Badge Clear

**Goal:** When the user opens a channel, call `POST /api/channels/:id/read` and clear the local unread badge immediately (optimistic). Re-increment the badge only when a new push_msg arrives while the channel is NOT active.

### 3.1 Modify `message.service.ts` — call markRead on loadMessages

**File:** `client/src/app/core/messages/message.service.ts`

Update `loadMessages` to also call `markRead`:

```typescript
/** Load the latest 50 messages for a channel and mark it as read. */
async loadMessages(channelId: number): Promise<void> {
  const msgs = await this.fetchMessages(channelId, { limit: 50 });
  this.messages.set([...msgs].reverse());
  this.activeChannelId.set(channelId);
  // Mark read optimistically — don't await to avoid delaying UI
  this.markRead(channelId).catch(err =>
    console.warn('[MessageService] markRead failed', err)
  );
  // Clear local unread badge immediately
  this.channelService.updateUnread(channelId, 0);
}
```

### 3.2 Increment unread only for inactive channels in `handlePush`

**File:** `client/src/app/core/messages/message.service.ts`

In `handlePush`, after the early return for inactive channels, increment the unread badge:

```typescript
private handlePush(pushed: PushMsgPayload): void {
  this.ws.updateChannelSeq(pushed.channel_id, pushed.seq);

  // Always update the channel preview (last message) in the sidebar
  this.channelService.updateLastMessage(pushed.channel_id, pushed.content, pushed.created_at);

  if (this.activeChannelId() !== pushed.channel_id) {
    // Not viewing this channel — increment the unread badge
    this.channelService.incrementUnread(pushed.channel_id);
    return;
  }

  // Channel is active — mark read immediately
  this.markRead(pushed.channel_id).catch(() => {});
  this.channelService.updateUnread(pushed.channel_id, 0);

  if (pushed.msg_type === 2) return;

  const alreadyPresent = this.messages().some(m => m.seq === pushed.seq);
  if (alreadyPresent) return;

  const msg: Message = {
    id: pushed.server_msg_id,
    channel_id: pushed.channel_id,
    seq: pushed.seq,
    sender_id: pushed.sender_id,
    msg_type: pushed.msg_type,
    content: pushed.content,
    visible_to: pushed.visible_to,
    created_at: pushed.created_at,
  };
  this.messages.update(msgs => [...msgs, msg]);
  // Signal that we should auto-scroll if near the bottom (Task 7)
  this._newMessageArrived.next();
}
```

### 3.3 Add `incrementUnread` and `updateLastMessage` to `ChannelService`

**File:** `client/src/app/core/channels/channel.service.ts`

```typescript
/** Increment unread count for a channel by 1 (called when push_msg arrives for inactive channel). */
incrementUnread(channelId: number): void {
  this.channels.update(channels =>
    channels.map(ch =>
      ch.id === channelId
        ? { ...ch, unread_count: (ch.unread_count ?? 0) + 1 }
        : ch
    )
  );
}

/** Update the last-message preview shown in the sidebar. */
updateLastMessage(channelId: number, content: string, at: string): void {
  this.channels.update(channels =>
    channels.map(ch =>
      ch.id === channelId
        ? { ...ch, last_msg_content: content, last_msg_at: at }
        : ch
    )
  );
}
```

### 3.4 Add `_newMessageArrived` Subject to `MessageService`

```typescript
import { Subject } from 'rxjs';

/** Emits when a new message arrives in the active channel (for scroll logic). */
readonly _newMessageArrived = new Subject<void>();
```

### Verification

- [ ] Opening a channel clears its red badge immediately
- [ ] Sending a message to channel B while viewing channel A increments A's badge
- [ ] Returning to channel A clears its badge
- [ ] `POST /api/channels/:id/read` is called on channel open (check Network tab)

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/core/messages/message.service.ts client/src/app/core/channels/channel.service.ts
git commit -m "feat(chat): auto mark-read on channel enter, unread badge increment on push"
```

---

## Task 4: DM Creation Flow (Contacts → Open/Create DM → Navigate to Chat)

**Goal:** On the Friends tab in Contacts, add a "Message" button next to each friend. Clicking it calls `POST /api/channels/dm` (already implemented in `ChannelService.createOrGetDM`), adds the channel to the local list with the peer's display_name as the channel name, then navigates to `/channels/:id`.

### 4.1 Modify `contacts.component.ts` — add `openDM()`

**File:** `client/src/app/features/contacts/contacts.component.ts`

```typescript
import { Router } from '@angular/router';
import { ChannelService } from '../../core/channels/channel.service';

// Inside class:
private router = inject(Router);
private channelService = inject(ChannelService);

openingDM = signal<number | null>(null);   // stores friend.id while in-flight

async openDM(friend: Friend): Promise<void> {
  if (this.openingDM() !== null) return;
  this.openingDM.set(friend.id);
  this.clearMessages();
  try {
    const channel = await this.channelService.createOrGetDM(friend.id);
    // Store peer display_name in channels list so the header can show it
    this.channelService.setDMPeerName(channel.id, friend.display_name);
    // Reload channel list to pick up any newly created channel
    await this.channelService.loadChannels();
    this.router.navigate(['channels', channel.id]);
  } catch (err: any) {
    this.actionError.set(err?.error?.error ?? 'Failed to open DM.');
  } finally {
    this.openingDM.set(null);
  }
}
```

### 4.2 Add `setDMPeerName` to `ChannelService`

**File:** `client/src/app/core/channels/channel.service.ts`

```typescript
/**
 * After creating/opening a DM, store the peer's display_name as the channel
 * label so the chat header and sidebar can show it without a separate API call.
 * Called immediately after createOrGetDM resolves.
 */
setDMPeerName(channelId: number, peerName: string): void {
  this.channels.update(channels => {
    const exists = channels.some(ch => ch.id === channelId);
    if (exists) {
      return channels.map(ch =>
        ch.id === channelId ? { ...ch, name: peerName } : ch
      );
    }
    // Channel not yet in list (loadChannels will add it) — nothing to do yet.
    return channels;
  });
}
```

> After `loadChannels()` completes in `openDM()`, the freshly-loaded channel will have `name: ''` again (the server doesn't store the peer name in the channel name). So we call `setDMPeerName` again after reload:

Update `openDM()` to call `setDMPeerName` after reload:

```typescript
async openDM(friend: Friend): Promise<void> {
  if (this.openingDM() !== null) return;
  this.openingDM.set(friend.id);
  this.clearMessages();
  try {
    const channel = await this.channelService.createOrGetDM(friend.id);
    await this.channelService.loadChannels();
    // Re-apply peer name after reload (server returns empty name for DMs)
    this.channelService.setDMPeerName(channel.id, friend.display_name);
    this.router.navigate(['channels', channel.id]);
  } catch (err: any) {
    this.actionError.set(err?.error?.error ?? 'Failed to open DM.');
  } finally {
    this.openingDM.set(null);
  }
}
```

### 4.3 Update `contacts.component.html` — "Message" button on Friends tab

**File:** `client/src/app/features/contacts/contacts.component.html`

Replace the friends list `<li>` (the one without actions) with:

```html
@for (friend of friendService.friends(); track friend.id) {
  <li class="user-row">
    <div class="avatar">{{ friend.display_name[0] | uppercase }}</div>
    <div class="info">
      <span class="display-name">{{ friend.display_name }}</span>
      <span class="username">&#64;{{ friend.username }}</span>
    </div>
    <div class="actions">
      <button
        class="btn-message"
        (click)="openDM(friend)"
        [disabled]="openingDM() === friend.id"
      >
        {{ openingDM() === friend.id ? '…' : 'Message' }}
      </button>
    </div>
  </li>
}
```

### 4.4 Add button styles to `contacts.component.scss`

```scss
.btn-message {
  padding: 0.3rem 0.75rem;
  background: #007aff;
  color: #fff;
  border: none;
  border-radius: 6px;
  font-size: 0.82rem;
  cursor: pointer;

  &:hover:not(:disabled) { background: #0062cc; }
  &:disabled { opacity: 0.5; cursor: not-allowed; }
}
```

### Verification

- [ ] Friends tab shows "Message" button next to each friend
- [ ] Clicking "Message" calls `POST /api/channels/dm` and navigates to the chat
- [ ] DM channel name in sidebar and header shows the friend's display_name
- [ ] Clicking "Message" again for the same friend re-uses the existing DM channel (idempotent API)

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/features/contacts/ client/src/app/core/channels/channel.service.ts
git commit -m "feat(contacts): open/create DM from friends list, navigate to chat"
```

---

## Task 5: Channel List Sorting + Search Filter

**Goal:**
1. Sort channels by `last_msg_at` descending (most recent activity first)
2. Add a search/filter input that filters by channel name (or DM peer name) in real-time
3. Show a subtle online status dot on DM avatars (stubbed — always gray until a presence system exists)

### 5.1 Add `sortedFilteredChannels` computed to `ChannelService`

**File:** `client/src/app/core/channels/channel.service.ts`

```typescript
/** The channel search query — set by ChannelListComponent */
readonly searchQuery = signal('');

/** Channels sorted by last_msg_at desc, filtered by searchQuery. */
readonly sortedFilteredChannels = computed(() => {
  const q = this.searchQuery().trim().toLowerCase();
  const list = [...this.channels()].sort((a, b) => {
    const ta = a.last_msg_at ? new Date(a.last_msg_at).getTime() : 0;
    const tb = b.last_msg_at ? new Date(b.last_msg_at).getTime() : 0;
    return tb - ta;
  });
  if (!q) return list;
  return list.filter(ch => this.channelLabel(ch).toLowerCase().includes(q));
});
```

### 5.2 Update `channel-list.component.ts`

**File:** `client/src/app/features/channel-list/channel-list.component.ts`

- Use `sortedFilteredChannels` instead of `channels()` in the template
- Bind search input to `channelService.searchQuery`
- Update `channelLabel` to use the service helper

```typescript
import { FormsModule } from '@angular/forms';

// Add FormsModule to imports array
imports: [CommonModule, RouterLink, CreateGroupComponent, FormsModule],

// Remove local channelLabel/previewText — delegate to service
channelLabel(ch: ChannelWithPreview): string {
  return this.channelService.channelLabel(ch);
}

previewText(ch: ChannelWithPreview): string {
  const msg = ch.last_msg_content;
  if (!msg) return 'No messages yet';
  return msg.length > 40 ? msg.slice(0, 40) + '…' : msg;
}
```

### 5.3 Update `channel-list.component.html`

**File:** `client/src/app/features/channel-list/channel-list.component.html`

Add search input between `.section-label` and `.channels`:

```html
<!-- Search / filter -->
<div class="search-bar">
  <input
    type="text"
    class="search-input"
    placeholder="Search chats…"
    [ngModel]="channelService.searchQuery()"
    (ngModelChange)="channelService.searchQuery.set($event)"
  />
</div>

<ul class="channels">
  @for (ch of channelService.sortedFilteredChannels(); track ch.id) {
    <li class="channel-row" (click)="openChannel(ch)">
      <div class="channel-avatar">
        {{ channelLabel(ch)[0] | uppercase }}
        @if (ch.type === 1) {
          <span class="online-dot"></span>
        }
      </div>
      <div class="channel-info">
        <div class="channel-name-row">
          <span class="channel-name">{{ channelLabel(ch) }}</span>
          <span class="channel-time">{{ lastMsgTime(ch) }}</span>
        </div>
        <div class="channel-preview-row">
          <span class="preview">{{ previewText(ch) }}</span>
          @if (ch.unread_count > 0) {
            <span class="badge">{{ ch.unread_count }}</span>
          }
        </div>
      </div>
    </li>
  } @empty {
    <li class="empty">
      @if (channelService.searchQuery()) {
        No chats match "{{ channelService.searchQuery() }}".
      } @else {
        No channels yet. Create a group or start a DM.
      }
    </li>
  }
</ul>
```

Add `lastMsgTime` to `channel-list.component.ts`:

```typescript
lastMsgTime(ch: ChannelWithPreview): string {
  if (!ch.last_msg_at) return '';
  const d = new Date(ch.last_msg_at);
  const now = new Date();
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
}
```

### 5.4 Update `channel-list.component.scss`

```scss
/* ---- search bar ---- */

.search-bar {
  padding: 0.5rem 0.75rem;
}

.search-input {
  width: 100%;
  box-sizing: border-box;
  background: #313244;
  border: 1px solid #45475a;
  border-radius: 6px;
  color: #cdd6f4;
  padding: 0.4rem 0.6rem;
  font-size: 0.85rem;
  outline: none;

  &::placeholder { color: #6c7086; }
  &:focus { border-color: #89b4fa; }
}

/* ---- channel row: move badge to preview row ---- */

.channel-preview-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.25rem;
}

.channel-time {
  font-size: 0.7rem;
  color: #6c7086;
  flex-shrink: 0;
}

/* ---- online dot ---- */

.channel-avatar {
  position: relative;  /* add to existing rule */
}

.online-dot {
  position: absolute;
  bottom: 0;
  right: 0;
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: #6c7086;   /* gray = offline/unknown; Plan N will set green when presence exists */
  border: 2px solid #1e1e2e;
}
```

### Verification

- [ ] Channels sorted with most-recently-active at the top
- [ ] Typing in search box filters channel list in real-time
- [ ] Clearing search shows all channels again
- [ ] DM channel avatars show a small dot (gray for now)
- [ ] Last-message time shown on each row

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/features/channel-list/ client/src/app/core/channels/channel.service.ts
git commit -m "feat(channel-list): sort by last message, search filter, DM online dot"
```

---

## Task 6: Reply-To Message Functionality

**Goal:** Right-click (or long-press) a message → context menu with "Reply" and "Copy". Selecting Reply sets a reply banner in the send box. The message is sent with `reply_to: <seq>`. Incoming messages with `reply_to` show a quoted preview above the bubble.

### 6.1 Add reply state to `chat.component.ts`

**File:** `client/src/app/features/chat/chat.component.ts`

```typescript
import { Message } from '../../core/messages/message.service';

/** The message the user is currently replying to. Cleared after send. */
readonly replyTarget = signal<Message | null>(null);

/** Context menu state */
readonly contextMenuMsg = signal<Message | null>(null);
readonly contextMenuPos = signal<{ x: number; y: number } | null>(null);

onContextMenu(event: MouseEvent, msg: Message): void {
  event.preventDefault();
  this.contextMenuMsg.set(msg);
  this.contextMenuPos.set({ x: event.clientX, y: event.clientY });
}

closeContextMenu(): void {
  this.contextMenuMsg.set(null);
  this.contextMenuPos.set(null);
}

replyToMessage(msg: Message): void {
  this.replyTarget.set(msg);
  this.closeContextMenu();
  // Focus the textarea
  setTimeout(() => {
    const ta = document.querySelector<HTMLTextAreaElement>('.message-input');
    ta?.focus();
  }, 0);
}

copyMessageText(msg: Message): void {
  navigator.clipboard.writeText(msg.content).catch(() => {});
  this.closeContextMenu();
}

cancelReply(): void {
  this.replyTarget.set(null);
}
```

Update `send()` to include `reply_to`:

```typescript
async send(): Promise<void> {
  const content = this.messageText().trim();
  if (!content || this.sending() || !this.channelId) return;

  const clientMsgId = crypto.randomUUID();
  const currentUser = this.auth.currentUser();
  const replyTo = this.replyTarget()?.seq ?? undefined;

  const optimistic: Message = {
    id: -1,
    channel_id: this.channelId,
    seq: -1,
    client_msg_id: clientMsgId,
    sender_id: currentUser?.id ?? 0,
    msg_type: 1,
    content,
    reply_to: replyTo,
    created_at: new Date().toISOString(),
  };

  this.messageService.appendOptimistic(optimistic);
  this.messageText.set('');
  this.replyTarget.set(null);   // clear reply after send
  this.shouldScrollToBottom = true;
  this.sending.set(true);
  this.error.set(null);

  try {
    const confirmed = await this.messageService.sendMessage(this.channelId, {
      content,
      client_msg_id: clientMsgId,
      msg_type: 1,
      reply_to: replyTo,
    });
    this.messageService.confirmSent(clientMsgId, confirmed);
    this.shouldScrollToBottom = true;
  } catch (err) {
    this.error.set('Failed to send message.');
    console.error(err);
    this.messageService.removeOptimistic(clientMsgId);
  } finally {
    this.sending.set(false);
  }
}
```

### 6.2 Add reply preview lookup helper

```typescript
/** Find the message being replied to for display in bubble */
getReplyTargetMsg(replySeq: number): Message | undefined {
  return this.messageService.messages().find(m => m.seq === replySeq);
}

getReplyTargetPreview(replySeq: number): string {
  const msg = this.getReplyTargetMsg(replySeq);
  if (!msg) return `Message #${replySeq}`;
  const name = this.messageService.getSenderName(msg.sender_id);
  const preview = msg.content.length > 60 ? msg.content.slice(0, 60) + '…' : msg.content;
  return `${name}: ${preview}`;
}
```

### 6.3 Update `chat.component.html`

Add context menu overlay and reply banner. Place at root of `.chat-window`:

```html
<!-- Global click dismiss for context menu -->
@if (contextMenuPos()) {
  <div class="context-overlay" (click)="closeContextMenu()"></div>
  <div
    class="context-menu"
    [style.left.px]="contextMenuPos()!.x"
    [style.top.px]="contextMenuPos()!.y"
  >
    <button (click)="replyToMessage(contextMenuMsg()!)">↩ Reply</button>
    <button (click)="copyMessageText(contextMenuMsg()!)">⎘ Copy</button>
  </div>
}
```

Update `.bubble-wrapper` in the message loop — replace the reply_to stub:

```html
@if (msg.reply_to) {
  <div class="reply-preview">
    {{ getReplyTargetPreview(msg.reply_to) }}
  </div>
}
```

Add reply banner above the send box:

```html
@if (replyTarget()) {
  <div class="reply-banner">
    <span class="reply-banner-text">
      ↩ Replying to: {{ replyTarget()!.content | slice:0:60 }}
    </span>
    <button class="reply-cancel" (click)="cancelReply()">✕</button>
  </div>
}
```

### 6.4 Add styles to `chat.component.scss`

```scss
/* ---- context menu ---- */

.context-overlay {
  position: fixed;
  inset: 0;
  z-index: 100;
}

.context-menu {
  position: fixed;
  z-index: 101;
  background: #fff;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
  overflow: hidden;
  min-width: 130px;

  button {
    display: block;
    width: 100%;
    padding: 0.5rem 1rem;
    text-align: left;
    background: none;
    border: none;
    font-size: 0.88rem;
    cursor: pointer;
    color: #111827;

    &:hover { background: #f3f4f6; }
  }
}

/* ---- reply banner ---- */

.reply-banner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.4rem 1rem;
  background: #eff6ff;
  border-top: 1px solid #bfdbfe;
  font-size: 0.82rem;
  color: #1d4ed8;
}

.reply-cancel {
  background: none;
  border: none;
  cursor: pointer;
  color: #6b7280;
  font-size: 0.9rem;
  padding: 0 0.25rem;

  &:hover { color: #111; }
}

/* ---- reply preview in bubble ---- */

.reply-preview {
  font-size: 0.72rem;
  color: #6b7280;
  background: #f3f4f6;
  border-left: 3px solid #9ca3af;
  padding: 0.2rem 0.5rem;
  border-radius: 4px;
  margin-bottom: 0.15rem;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.message-group.mine .reply-preview {
  background: rgba(255, 255, 255, 0.15);
  border-left-color: rgba(255, 255, 255, 0.5);
  color: rgba(255, 255, 255, 0.8);
}
```

### Verification

- [ ] Right-clicking a message shows context menu with "Reply" and "Copy"
- [ ] Selecting "Reply" shows a banner above the send box with the quoted message
- [ ] Sending a reply sets `reply_to` in the payload (check Network tab)
- [ ] Messages with `reply_to` show a quoted preview above the bubble
- [ ] "Copy" copies the message text to clipboard
- [ ] Clicking outside the context menu dismisses it

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/features/chat/
git commit -m "feat(chat): reply-to message with context menu, reply banner, quoted preview"
```

---

## Task 7: New Messages Indicator + Jump to Bottom

**Goal:** When the user has scrolled up and a new message arrives, show a "↓ New messages" button at the bottom of the chat. Clicking it scrolls to the bottom. Hide the button when the user is already at (or near) the bottom.

### 7.1 Add scroll-tracking state to `chat.component.ts`

**File:** `client/src/app/features/chat/chat.component.ts`

```typescript
import { Subject } from 'rxjs';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { DestroyRef } from '@angular/core';

private destroyRef = inject(DestroyRef);

/** True when the user has scrolled up and is not at the bottom. */
readonly isScrolledUp = signal(false);

/** True when there are unread messages below the current scroll position. */
readonly hasNewMessagesBelow = signal(false);

private readonly BOTTOM_THRESHOLD = 80; // px from bottom to consider "at bottom"
```

Update `ngOnInit` to subscribe to new message events:

```typescript
ngOnInit(): void {
  this.route.paramMap.subscribe(params => {
    const id = Number(params.get('id'));
    if (id && id !== this.channelId) {
      this.channelId = id;
      this.isScrolledUp.set(false);
      this.hasNewMessagesBelow.set(false);
      this.loadChannel(id);
    }
  });

  // When a new message arrives and we're scrolled up, show the indicator
  this.messageService._newMessageArrived
    .pipe(takeUntilDestroyed(this.destroyRef))
    .subscribe(() => {
      if (this.isScrolledUp()) {
        this.hasNewMessagesBelow.set(true);
      }
    });
}
```

Update `onScroll` to track scroll position:

```typescript
onScroll(event: Event): void {
  const el = event.target as HTMLElement;
  const distFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
  const atBottom = distFromBottom < this.BOTTOM_THRESHOLD;

  this.isScrolledUp.set(!atBottom);
  if (atBottom) {
    this.hasNewMessagesBelow.set(false);
  }

  if (el.scrollTop === 0) {
    this.onScrolledToTop();
  }
}
```

Add `jumpToBottom` method:

```typescript
jumpToBottom(): void {
  this.scrollToBottom();
  this.hasNewMessagesBelow.set(false);
  this.isScrolledUp.set(false);
}
```

Update `ngAfterViewChecked`: only auto-scroll if not scrolled up:

```typescript
ngAfterViewChecked(): void {
  if (this.shouldScrollToBottom) {
    if (!this.isScrolledUp()) {
      this.scrollToBottom();
    }
    this.shouldScrollToBottom = false;
  }
}
```

> **Note:** The initial channel load should always scroll to bottom regardless:

```typescript
private async loadChannel(channelId: number): Promise<void> {
  this.error.set(null);
  this.replyTarget.set(null);
  this.isScrolledUp.set(false);
  try {
    await Promise.all([
      this.messageService.loadMessages(channelId),
      this.channelService.loadMemberCount(channelId),
    ]);
    // Force scroll to bottom on fresh channel load
    this.shouldScrollToBottom = true;
  } catch (err) {
    this.error.set('Failed to load messages.');
    console.error(err);
  }
}
```

Override in `ngAfterViewChecked` with a force flag:

```typescript
private forceScrollBottom = false;

private async loadChannel(channelId: number): Promise<void> {
  // ...
  this.forceScrollBottom = true;
  this.shouldScrollToBottom = true;
}

ngAfterViewChecked(): void {
  if (this.shouldScrollToBottom) {
    if (this.forceScrollBottom || !this.isScrolledUp()) {
      this.scrollToBottom();
      this.forceScrollBottom = false;
    }
    this.shouldScrollToBottom = false;
  }
}
```

### 7.2 Update `chat.component.html` — new messages button

Add the jump button inside `.chat-window`, after `.message-list`:

```html
@if (hasNewMessagesBelow()) {
  <button class="jump-to-bottom" (click)="jumpToBottom()">
    ↓ New messages
  </button>
}
```

### 7.3 Add styles to `chat.component.scss`

```scss
/* ---- jump to bottom button ---- */

.jump-to-bottom {
  position: absolute;
  bottom: 80px;   /* above send box */
  left: 50%;
  transform: translateX(-50%);
  background: #007aff;
  color: #fff;
  border: none;
  border-radius: 20px;
  padding: 0.4rem 1rem;
  font-size: 0.82rem;
  cursor: pointer;
  box-shadow: 0 2px 8px rgba(0, 122, 255, 0.4);
  white-space: nowrap;
  z-index: 10;

  &:hover { background: #0062cc; }
}
```

Also set `.chat-window` to `position: relative` so the absolute button is positioned correctly:

```scss
.chat-window {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: #fff;
  position: relative;   // add this
}
```

### Verification

- [ ] Scroll up in a long channel: "New messages" button does NOT appear unless a new message actually arrives
- [ ] While scrolled up, receive a new message (from another terminal/user): "↓ New messages" button appears
- [ ] Clicking the button scrolls to the bottom and hides the button
- [ ] Scrolling back to the bottom manually also hides the button
- [ ] New channel open always scrolls to bottom regardless of previous scroll position

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/features/chat/
git commit -m "feat(chat): new messages indicator when scrolled up, jump-to-bottom button"
```

---

## Task 8: Integration Verification

**Goal:** Smoke-test all 7 tasks end-to-end. Fix any wiring issues, import errors, or broken templates found during integration.

### 8.1 Build check

```bash
cd /Users/mac17/workspace/ai/im/client
npm run build 2>&1 | tail -30
```

Fix any TypeScript or template compilation errors before proceeding.

### 8.2 Integration checklist

Run the dev server:

```bash
cd /Users/mac17/workspace/ai/im/client
npm start
```

Test each feature in order:

**Task 1 — Chat header**
- [ ] Open any group channel → header shows name, member count ("N members"), gear icon
- [ ] Open a DM → header shows peer's display_name (set after Task 4) or "DM"
- [ ] Gear icon → navigates to settings page and back

**Task 2 — Message grouping**
- [ ] Send 3 messages in a row quickly → grouped under one avatar
- [ ] Receive a message from another user → shows their avatar + name above the group
- [ ] Day boundary (or manually change created_at in DB) → date separator appears

**Task 3 — Mark read**
- [ ] Open channel with unread badge → badge clears immediately
- [ ] Background channel receives push → badge increments
- [ ] Switching to that channel → badge clears again

**Task 4 — DM creation**
- [ ] Contacts → Friends → click "Message" on a friend → navigated to DM chat
- [ ] Channel name in header and sidebar shows friend's display_name
- [ ] Click "Message" again → same DM channel opened (no duplicate)

**Task 5 — Channel list**
- [ ] Channel list sorted by most-recent message at top (send a message → channel moves to top)
- [ ] Search "te" → filters to channels whose name contains "te"
- [ ] Clear search → all channels shown

**Task 6 — Reply**
- [ ] Right-click a message → context menu appears
- [ ] Click Reply → reply banner appears in send box
- [ ] Send message → Network tab shows `reply_to` in request payload
- [ ] Replied message has quoted preview above bubble

**Task 7 — Scroll**
- [ ] Scroll to top → no "New messages" button
- [ ] Scroll up (not to top), receive new message → "↓ New messages" button appears
- [ ] Click button → scrolls to bottom, button disappears

### 8.3 Fix circular dependency risk

`MessageService` injects `ChannelService`, and `ChannelService` injects `WebSocketService`. Verify no circular injection:

```bash
cd /Users/mac17/workspace/ai/im/client
ng build --configuration development 2>&1 | grep -i circular
```

If circular deps are reported, move `FriendService` injection out of `MessageService.getSenderName()` and instead pass friends as a parameter from the component layer.

Alternative (if circular): inject `FriendService` in `ChatComponent` and pass a `getSenderName` callback:

```typescript
// In chat.component.ts
private friendService = inject(FriendService);

getSenderName = (senderId: number): string => {
  const me = this.auth.currentUser();
  if (me && senderId === me.id) return 'You';
  return this.friendService.friends().find(f => f.id === senderId)?.display_name ?? `User ${senderId}`;
};
```

Then pass as input to the `groupedMessages` computed (remove `getSenderName` from `MessageService`).

### 8.4 Final commit

```bash
cd /Users/mac17/workspace/ai/im
git add -p   # stage only changed files
git commit -m "fix(plan8): integration fixes — import wiring, circular deps, template errors"
```

---

## Summary of Files Changed

| File | Change |
|------|--------|
| `client/src/app/core/channels/channel.service.ts` | `channelLabel()`, `memberCounts`, `loadMemberCount()`, `sortedFilteredChannels`, `searchQuery`, `incrementUnread()`, `updateLastMessage()`, `setDMPeerName()` |
| `client/src/app/core/messages/message.service.ts` | `getSenderName()`, `_newMessageArrived` Subject, `loadMessages` → markRead, `handlePush` → increment/clear unread |
| `client/src/app/features/chat/chat.component.ts` | Header data, `groupedMessages`, `replyTarget`, context menu, scroll sentinel, `jumpToBottom`, `forceScrollBottom` |
| `client/src/app/features/chat/chat.component.html` | Chat header, grouped messages, date separators, system messages, reply preview, context menu, reply banner, jump button |
| `client/src/app/features/chat/chat.component.scss` | Header, groups, date separator, context menu, reply, jump button |
| `client/src/app/features/channel-list/channel-list.component.ts` | `sortedFilteredChannels`, `lastMsgTime()`, delegate `channelLabel` to service |
| `client/src/app/features/channel-list/channel-list.component.html` | Search input, time column, preview row with badge, online dot |
| `client/src/app/features/channel-list/channel-list.component.scss` | Search bar, preview row, time, online dot |
| `client/src/app/features/contacts/contacts.component.ts` | `openDM()`, `openingDM` signal |
| `client/src/app/features/contacts/contacts.component.html` | "Message" button on friends row |
| `client/src/app/features/contacts/contacts.component.scss` | `.btn-message` styles |
