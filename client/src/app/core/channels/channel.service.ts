import { Injectable, inject, signal, computed } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';
import { WebSocketService } from '../ws/websocket.service';
import { ReadSyncPayload } from '../ws/websocket.models';
import { FriendService } from '../friends/friend.service';

// ---------- types ----------

export interface Channel {
  id: number;
  type: number;       // 1=DM, 2=GROUP
  name: string;
  avatar_url: string;
  seq: number;
  creator_id: number | null;
  created_at: string;
  updated_at: string;
}

export interface ChannelWithPreview extends Channel {
  last_msg_content: string;
  last_msg_at: string;
  unread_count: number;
}

export interface ChannelMember {
  user_id: number;
  channel_id: number;
  role: number;       // 1=member, 2=admin, 3=owner
  last_read_seq: number;
  phantom_count: number;
  phantom_at_read: number;
  joined_at: string;
}

const API_BASE = 'http://localhost:8080/api';

@Injectable({ providedIn: 'root' })
export class ChannelService {
  /** Reactive signal: channel list with preview info */
  readonly channels = signal<ChannelWithPreview[]>([]);

  /** Cached member counts keyed by channel id — loaded on demand by ChatComponent */
  readonly memberCounts = signal<Record<number, number>>({});

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

  private ws = inject(WebSocketService);
  private friendService = inject(FriendService);

  constructor(private http: HttpClient) {
    // When another device marks a channel as read, update our local unread count.
    this.ws.readSync$.subscribe(event => this.handleReadSync(event));
  }

  // ---------- channel operations ----------

  async createGroup(name: string, memberIds: number[]): Promise<Channel> {
    return firstValueFrom(
      this.http.post<Channel>(`${API_BASE}/channels`, { name, member_ids: memberIds })
    );
  }

  async createOrGetDM(peerId: number): Promise<Channel> {
    return firstValueFrom(
      this.http.post<Channel>(`${API_BASE}/channels/dm`, { peer_id: peerId })
    );
  }

  async getChannel(id: number): Promise<Channel> {
    return firstValueFrom(
      this.http.get<Channel>(`${API_BASE}/channels/${id}`)
    );
  }

  async updateChannel(id: number, name: string, avatarUrl: string): Promise<Channel> {
    return firstValueFrom(
      this.http.put<Channel>(`${API_BASE}/channels/${id}`, { name, avatar_url: avatarUrl })
    );
  }

  async addMember(channelId: number, userId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/channels/${channelId}/members`, { user_id: userId })
    );
  }

  async removeMember(channelId: number, userId: number): Promise<void> {
    await firstValueFrom(
      this.http.delete(`${API_BASE}/channels/${channelId}/members/${userId}`)
    );
  }

  async listMembers(channelId: number): Promise<ChannelMember[]> {
    return firstValueFrom(
      this.http.get<ChannelMember[]>(`${API_BASE}/channels/${channelId}/members`)
    );
  }

  async leaveChannel(channelId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/channels/${channelId}/leave`, {})
    );
    await this.loadChannels();
  }

  /**
   * Returns a human-readable label for any channel.
   * - GROUP (type 2): uses channel.name
   * - DM (type 1): channel.name is set to peer display_name by the create-DM flow (Task 4).
   *   If blank (legacy channels), falls back to "DM".
   */
  channelLabel(ch: ChannelWithPreview): string {
    if (ch.type === 2) return ch.name || 'Group';
    return ch.name || 'DM';
  }

  /**
   * After creating/opening a DM, store the peer's display_name as the channel
   * label so the chat header and sidebar can show it without a separate API call.
   * Called immediately after createOrGetDM resolves (and again after loadChannels).
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

  async loadMemberCount(channelId: number): Promise<void> {
    try {
      const members = await this.listMembers(channelId);
      this.memberCounts.update(counts => ({ ...counts, [channelId]: members.length }));
    } catch {
      // non-fatal: header shows nothing if count fails
    }
  }

  // ---------- data loading ----------

  async loadChannels(): Promise<void> {
    const data = await firstValueFrom(
      this.http.get<ChannelWithPreview[]>(`${API_BASE}/channels`)
    );
    this.channels.set(data ?? []);
  }

  /** Update the unread count for a single channel (called after batch sync or mark-read). */
  updateUnread(channelId: number, unread: number): void {
    this.channels.update(channels =>
      channels.map(ch =>
        ch.id === channelId ? { ...ch, unread_count: unread } : ch
      )
    );
  }

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

  private handleReadSync(event: ReadSyncPayload): void {
    // Update the channel's unread count to 0 (the other device read everything
    // up to event.read_seq). Any messages arriving after read_seq will increment
    // the count again via push_msg.
    this.channels.update(channels =>
      channels.map(ch =>
        ch.id === event.channel_id
          ? { ...ch, unread_count: 0 }
          : ch
      )
    );
  }
}
