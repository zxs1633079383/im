import { Component, inject, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { Router, RouterLink } from '@angular/router';
import { ChannelService, ChannelWithPreview } from '../../core/channels/channel.service';
import { AuthService } from '../../core/auth/auth.service';
import { CreateGroupComponent } from '../create-group/create-group.component';

@Component({
  selector: 'app-channel-list',
  standalone: true,
  imports: [CommonModule, RouterLink, CreateGroupComponent],
  templateUrl: './channel-list.component.html',
  styleUrl: './channel-list.component.scss',
})
export class ChannelListComponent {
  channelService = inject(ChannelService);
  private auth = inject(AuthService);
  private router = inject(Router);

  showCreateGroup = signal(false);

  channelLabel(ch: ChannelWithPreview): string {
    if (ch.type === 2) {
      return ch.name || 'Group';
    }
    // DM: show "DM" until Plan 5 resolves peer name
    return 'DM';
  }

  previewText(ch: ChannelWithPreview): string {
    const msg = ch.last_msg_content;
    if (!msg) return 'No messages yet';
    return msg.length > 40 ? msg.slice(0, 40) + '…' : msg;
  }

  openChannel(ch: ChannelWithPreview): void {
    // Placeholder: in Plan 5 this will open the message view
    // For now navigate to settings as a stub
    this.router.navigate(['channels', ch.id, 'settings']);
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
