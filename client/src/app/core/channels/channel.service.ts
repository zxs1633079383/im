import { Injectable, inject, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';
import { WebSocketService } from '../ws/websocket.service';
import { ReadSyncPayload } from '../ws/websocket.models';

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

  private ws = inject(WebSocketService);

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

  // ---------- data loading ----------

  async loadChannels(): Promise<void> {
    const data = await firstValueFrom(
      this.http.get<ChannelWithPreview[]>(`${API_BASE}/channels`)
    );
    this.channels.set(data ?? []);
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
