import { Component, signal, inject } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { Router } from '@angular/router';
import { SearchService, MessageResult, UserResult } from '../../core/search/search.service';
import { Channel } from '../../core/channels/channel.service';

type TabType = 'messages' | 'users' | 'channels';

@Component({
  selector: 'app-search',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './search.component.html',
  styleUrls: ['./search.component.scss'],
})
export class SearchComponent {
  private searchService = inject(SearchService);
  private router = inject(Router);

  query = signal('');
  activeTab = signal<TabType>('messages');
  loading = signal(false);
  error = signal<string | null>(null);

  messageResults = signal<MessageResult[]>([]);
  userResults = signal<UserResult[]>([]);
  channelResults = signal<Channel[]>([]);

  private debounceTimer: ReturnType<typeof setTimeout> | null = null;

  onQueryChange(value: string): void {
    this.query.set(value);
    if (this.debounceTimer) clearTimeout(this.debounceTimer);
    if (!value.trim()) {
      this.messageResults.set([]);
      this.userResults.set([]);
      this.channelResults.set([]);
      return;
    }
    this.debounceTimer = setTimeout(() => this.runSearch(), 300);
  }

  setTab(tab: TabType): void {
    this.activeTab.set(tab);
  }

  async runSearch(): Promise<void> {
    const q = this.query().trim();
    if (!q) return;
    this.loading.set(true);
    this.error.set(null);
    try {
      const resp = await this.searchService.search(q);
      this.messageResults.set(resp.messages ?? []);
      this.userResults.set(resp.users ?? []);
      this.channelResults.set(resp.channels ?? []);
    } catch {
      this.error.set('Search failed. Please try again.');
    } finally {
      this.loading.set(false);
    }
  }

  goToMessage(msg: MessageResult): void {
    this.router.navigate(['/channels', msg.channel_id], {
      queryParams: { around_seq: msg.seq },
    });
  }

  goToChannel(channel: Channel): void {
    this.router.navigate(['/channels', channel.id]);
  }

  openDM(user: UserResult): void {
    this.router.navigate(['/contacts'], { queryParams: { dm_user_id: user.id } });
  }

  tabCount(tab: TabType): number {
    switch (tab) {
      case 'messages': return this.messageResults().length;
      case 'users': return this.userResults().length;
      case 'channels': return this.channelResults().length;
    }
  }
}
