import { Component, inject, signal, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { FriendService, UserSearchResult } from '../../core/friends/friend.service';

@Component({
  selector: 'app-contacts',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './contacts.component.html',
  styleUrl: './contacts.component.scss',
})
export class ContactsComponent implements OnInit {
  friendService = inject(FriendService);

  /** Controls which tab is active: 'friends' | 'pending' | 'search' */
  activeTab = signal<'friends' | 'pending' | 'search'>('friends');

  searchQuery = signal('');
  searchResults = signal<UserSearchResult[]>([]);
  searching = signal(false);
  searchError = signal('');

  actionError = signal('');
  actionSuccess = signal('');

  async ngOnInit(): Promise<void> {
    await Promise.all([
      this.friendService.loadFriends(),
      this.friendService.loadPendingRequests(),
    ]);
  }

  setTab(tab: 'friends' | 'pending' | 'search'): void {
    this.activeTab.set(tab);
    this.clearMessages();
  }

  async onSearch(): Promise<void> {
    const q = this.searchQuery().trim();
    this.searching.set(true);
    this.searchError.set('');
    try {
      const results = await this.friendService.searchUsers(q);
      this.searchResults.set(results);
    } catch {
      this.searchError.set('Search failed. Please try again.');
    } finally {
      this.searching.set(false);
    }
  }

  async addFriend(userId: number): Promise<void> {
    this.clearMessages();
    try {
      await this.friendService.sendRequest(userId);
      this.actionSuccess.set('Friend request sent!');
    } catch (err: any) {
      const msg = err?.error?.error ?? 'Failed to send request.';
      this.actionError.set(msg);
    }
  }

  async accept(friendshipId: number): Promise<void> {
    this.clearMessages();
    try {
      await this.friendService.acceptRequest(friendshipId);
      this.actionSuccess.set('Friend request accepted!');
    } catch {
      this.actionError.set('Failed to accept request.');
    }
  }

  async reject(friendshipId: number): Promise<void> {
    this.clearMessages();
    try {
      await this.friendService.rejectRequest(friendshipId);
    } catch {
      this.actionError.set('Failed to reject request.');
    }
  }

  private clearMessages(): void {
    this.actionError.set('');
    this.actionSuccess.set('');
  }
}
