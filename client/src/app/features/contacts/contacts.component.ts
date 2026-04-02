import { Component, inject, signal, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { Router } from '@angular/router';
import { FriendService, Friend, UserSearchResult } from '../../core/friends/friend.service';
import { ChannelService } from '../../core/channels/channel.service';

@Component({
  selector: 'app-contacts',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './contacts.component.html',
  styleUrl: './contacts.component.scss',
})
export class ContactsComponent implements OnInit {
  friendService = inject(FriendService);
  private router = inject(Router);
  private channelService = inject(ChannelService);

  /** Controls which tab is active: 'friends' | 'pending' | 'search' */
  activeTab = signal<'friends' | 'pending' | 'search'>('friends');

  /** Stores the friend.id that is currently being opened as a DM (in-flight). */
  openingDM = signal<number | null>(null);

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

  private clearMessages(): void {
    this.actionError.set('');
    this.actionSuccess.set('');
  }
}
