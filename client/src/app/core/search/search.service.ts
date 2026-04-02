import { Injectable, inject } from '@angular/core';
import { HttpClient, HttpParams } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';
import { Message } from '../messages/message.service';
import { Channel } from '../channels/channel.service';

const API_BASE = 'http://localhost:8080/api';

export interface UserResult {
  id: number;
  username: string;
  display_name: string;
  avatar_url: string;
}

export interface MessageResult extends Message {
  channel_name: string;
}

export interface SearchResponse {
  messages?: MessageResult[];
  users?: UserResult[];
  channels?: Channel[];
}

export type SearchType = 'messages' | 'users' | 'channels' | '';

@Injectable({ providedIn: 'root' })
export class SearchService {
  private http = inject(HttpClient);

  async search(
    q: string,
    type: SearchType = '',
    channelId?: number,
    limit = 20,
  ): Promise<SearchResponse> {
    if (!q.trim()) return {};
    let params = new HttpParams().set('q', q.trim()).set('limit', limit);
    if (type) params = params.set('type', type);
    if (channelId) params = params.set('channel_id', channelId);
    return firstValueFrom(
      this.http.get<SearchResponse>(`${API_BASE}/search`, { params }),
    );
  }

  async searchMessages(q: string, channelId?: number): Promise<MessageResult[]> {
    const r = await this.search(q, 'messages', channelId);
    return r.messages ?? [];
  }

  async searchUsers(q: string): Promise<UserResult[]> {
    const r = await this.search(q, 'users');
    return r.users ?? [];
  }

  async searchChannels(q: string): Promise<Channel[]> {
    const r = await this.search(q, 'channels');
    return r.channels ?? [];
  }
}
