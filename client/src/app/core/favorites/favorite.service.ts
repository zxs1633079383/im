import { Injectable, inject, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';
import { Message } from '../messages/message.service';
import { API_BASE } from '../config/api.config';

export interface FavoriteWithMessage {
  user_id: number;
  message_id: number;
  created_at: string;
  message: Message;
}

@Injectable({ providedIn: 'root' })
export class FavoriteService {
  private http = inject(HttpClient);

  /** Cached list of favorites — loaded on demand. */
  readonly favorites = signal<FavoriteWithMessage[]>([]);

  async add(messageId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/favorites/${messageId}`, {}),
    );
    await this.load(); // refresh list
  }

  async remove(messageId: number): Promise<void> {
    await firstValueFrom(
      this.http.delete(`${API_BASE}/favorites/${messageId}`),
    );
    this.favorites.update(favs => favs.filter(f => f.message_id !== messageId));
  }

  async load(): Promise<void> {
    const resp = await firstValueFrom(
      this.http.get<{ favorites: FavoriteWithMessage[] }>(`${API_BASE}/favorites`),
    );
    this.favorites.set(resp.favorites ?? []);
  }

  isFavorited(messageId: number): boolean {
    return this.favorites().some(f => f.message_id === messageId);
  }

  async forward(messageId: number, targetChannelIds: number[]): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/messages/forward`, {
        message_id: messageId,
        target_channel_ids: targetChannelIds,
      }),
    );
  }
}
