import { Injectable, signal, inject } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';
import { WebSocketService } from '../ws/websocket.service';

// ---------- types ----------

export interface Friend {
  id: number;
  username: string;
  display_name: string;
  avatar_url: string;
  status: number;
}

export interface PendingRequest {
  id: number;
  requester_id: number;
  addressee_id: number;
  status: number;
  created_at: string;
  updated_at: string;
  requester: Friend;
}

export interface UserSearchResult {
  id: number;
  username: string;
  display_name: string;
  avatar_url: string;
  status: number;
}

const API_BASE = 'http://localhost:8080/api';

@Injectable({ providedIn: 'root' })
export class FriendService {
  /** Reactive signal: accepted friend list */
  readonly friends = signal<Friend[]>([]);

  /** Reactive signal: incoming pending requests */
  readonly pendingRequests = signal<PendingRequest[]>([]);

  /** Reactive signal: pending request count for visual indicator. */
  readonly pendingCount = signal(0);

  private ws = inject(WebSocketService);

  constructor(private http: HttpClient) {
    // Subscribe to real-time friend events from WebSocket.
    this.ws.friendEvent$.subscribe(event => {
      if (event.event_type === 'request') {
        // New incoming friend request — reload pending list
        this.loadPendingRequests().catch(() => {});
      } else if (event.event_type === 'accepted') {
        // Friend accepted — reload both lists
        this.loadFriends().catch(() => {});
        this.loadPendingRequests().catch(() => {});
      }
    });
  }

  // ---------- friend operations ----------

  async sendRequest(addresseeId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/friends/request`, { addressee_id: addresseeId })
    );
  }

  async acceptRequest(friendshipId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/friends/accept`, { friendship_id: friendshipId })
    );
    await this.loadPendingRequests();
    await this.loadFriends();
  }

  async rejectRequest(friendshipId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/friends/reject`, { friendship_id: friendshipId })
    );
    await this.loadPendingRequests();
  }

  async blockUser(userId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/friends/block`, { user_id: userId })
    );
    await this.loadFriends();
  }

  // ---------- data loading ----------

  async loadFriends(): Promise<void> {
    const data = await firstValueFrom(
      this.http.get<Friend[]>(`${API_BASE}/friends`)
    );
    this.friends.set(data);
  }

  async loadPendingRequests(): Promise<void> {
    const data = await firstValueFrom(
      this.http.get<PendingRequest[]>(`${API_BASE}/friends/pending`)
    );
    this.pendingRequests.set(data);
    this.pendingCount.set(data?.length ?? 0);
  }

  async searchUsers(q: string): Promise<UserSearchResult[]> {
    return firstValueFrom(
      this.http.get<UserSearchResult[]>(`${API_BASE}/users/search`, {
        params: { q },
      })
    );
  }
}
