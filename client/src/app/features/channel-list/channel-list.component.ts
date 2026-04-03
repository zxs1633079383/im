import { Component, inject, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { Router, RouterLink } from '@angular/router';
import { ChannelService, ChannelWithPreview } from '../../core/channels/channel.service';
import { AuthService } from '../../core/auth/auth.service';
import { CreateGroupComponent } from '../create-group/create-group.component';
import { I18nService } from '../../core/i18n/i18n.service';

@Component({
  selector: 'app-channel-list',
  standalone: true,
  imports: [CommonModule, RouterLink, CreateGroupComponent, FormsModule],
  templateUrl: './channel-list.component.html',
  styleUrl: './channel-list.component.scss',
})
export class ChannelListComponent {
  channelService = inject(ChannelService);
  private auth = inject(AuthService);
  private router = inject(Router);
  private i18n = inject(I18nService);

  showCreateGroup = signal(false);

  t(key: string): string {
    return this.i18n.t(key);
  }

  channelLabel(ch: ChannelWithPreview): string {
    return this.channelService.channelLabel(ch);
  }

  previewText(ch: ChannelWithPreview): string {
    const msg = ch.last_msg_content;
    if (!msg) return this.i18n.t('chat.noMessages');
    return msg.length > 40 ? msg.slice(0, 40) + '…' : msg;
  }

  lastMsgTime(ch: ChannelWithPreview): string {
    if (!ch.last_msg_at) return '';
    const d = new Date(ch.last_msg_at);
    const now = new Date();
    if (d.toDateString() === now.toDateString()) {
      return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    }
    return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
  }

  openChannel(ch: ChannelWithPreview): void {
    this.router.navigate(['channels', ch.id]);
  }

  openCreateGroup(): void {
    this.showCreateGroup.set(true);
  }

  onGroupCreated(): void {
    this.showCreateGroup.set(false);
    this.channelService.loadChannels();
  }

  onGroupCancelled(): void {
    this.showCreateGroup.set(false);
  }

  logout(): void {
    this.auth.logout();
    this.router.navigate(['/login']);
  }
}
