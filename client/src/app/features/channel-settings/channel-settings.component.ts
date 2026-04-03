import { Component, inject, signal, computed, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { ChannelService, Channel, ChannelMember } from '../../core/channels/channel.service';
import { AuthService } from '../../core/auth/auth.service';
import { FriendService } from '../../core/friends/friend.service';

@Component({
  selector: 'app-channel-settings',
  standalone: true,
  imports: [CommonModule, FormsModule, RouterLink],
  templateUrl: './channel-settings.component.html',
  styleUrl: './channel-settings.component.scss',
})
export class ChannelSettingsComponent implements OnInit {
  private route = inject(ActivatedRoute);
  private router = inject(Router);
  private channelService = inject(ChannelService);
  auth = inject(AuthService);
  friendService = inject(FriendService);

  channel = signal<Channel | null>(null);
  members = signal<ChannelMember[]>([]);
  loading = signal(true);
  error = signal('');
  success = signal('');

  // Edit name state
  editName = signal('');
  savingName = signal(false);

  // Add member state
  addingMember = signal(false);
  showAddPicker = signal(false);

  /** Friends who are NOT already members of this channel */
  availableFriends = computed(() => {
    const memberIds = new Set(this.members().map(m => m.user_id));
    return this.friendService.friends().filter(f => !memberIds.has(f.id));
  });

  private channelId = 0;

  async ngOnInit(): Promise<void> {
    const id = Number(this.route.snapshot.paramMap.get('id'));
    this.channelId = id;
    await Promise.all([
      this.reload(),
      this.friendService.loadFriends(),
    ]);
  }

  private async reload(): Promise<void> {
    this.loading.set(true);
    this.error.set('');
    try {
      const [ch, members] = await Promise.all([
        this.channelService.getChannel(this.channelId),
        this.channelService.listMembers(this.channelId),
      ]);
      this.channel.set(ch);
      this.members.set(members);
      this.editName.set(ch.name);
    } catch {
      this.error.set('Failed to load channel.');
    } finally {
      this.loading.set(false);
    }
  }

  get myMember(): ChannelMember | undefined {
    const me = this.auth.currentUser();
    return this.members().find((m) => m.user_id === me?.id);
  }

  get isAdminOrOwner(): boolean {
    return (this.myMember?.role ?? 0) >= 2;
  }

  get isOwner(): boolean {
    return (this.myMember?.role ?? 0) === 3;
  }

  roleName(role: number): string {
    return role === 3 ? 'Owner' : role === 2 ? 'Admin' : 'Member';
  }

  async saveName(): Promise<void> {
    const name = this.editName().trim();
    if (!name) return;
    this.savingName.set(true);
    this.error.set('');
    try {
      const updated = await this.channelService.updateChannel(this.channelId, name, '');
      this.channel.set(updated);
      this.success.set('Channel name updated.');
    } catch {
      this.error.set('Failed to update name.');
    } finally {
      this.savingName.set(false);
    }
  }

  async addMember(userId: number, displayName: string): Promise<void> {
    this.addingMember.set(true);
    this.error.set('');
    try {
      await this.channelService.addMember(this.channelId, userId);
      this.success.set(`已添加 ${displayName}`);
      await this.reload();
    } catch (err: any) {
      this.error.set(err?.error?.error ?? '添加失败');
    } finally {
      this.addingMember.set(false);
    }
  }

  async removeMember(userId: number): Promise<void> {
    this.error.set('');
    try {
      await this.channelService.removeMember(this.channelId, userId);
      this.success.set('Member removed.');
      await this.reload();
    } catch (err: any) {
      this.error.set(err?.error?.error ?? 'Failed to remove member.');
    }
  }

  async leave(): Promise<void> {
    this.error.set('');
    try {
      await this.channelService.leaveChannel(this.channelId);
      this.router.navigate(['/']);
    } catch (err: any) {
      this.error.set(err?.error?.error ?? 'Failed to leave channel.');
    }
  }
}
