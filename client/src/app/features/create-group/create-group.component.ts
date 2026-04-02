import { Component, inject, signal, Output, EventEmitter } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ChannelService } from '../../core/channels/channel.service';
import { FriendService } from '../../core/friends/friend.service';

@Component({
  selector: 'app-create-group',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './create-group.component.html',
  styleUrl: './create-group.component.scss',
})
export class CreateGroupComponent {
  @Output() created = new EventEmitter<void>();
  @Output() cancelled = new EventEmitter<void>();

  private channelService = inject(ChannelService);
  friendService = inject(FriendService);

  groupName = signal('');
  selectedIds = signal<Set<number>>(new Set());
  creating = signal(false);
  error = signal('');

  toggleMember(id: number): void {
    const set = new Set(this.selectedIds());
    if (set.has(id)) {
      set.delete(id);
    } else {
      set.add(id);
    }
    this.selectedIds.set(set);
  }

  isSelected(id: number): boolean {
    return this.selectedIds().has(id);
  }

  async onCreate(): Promise<void> {
    const name = this.groupName().trim();
    if (!name) {
      this.error.set('Group name is required.');
      return;
    }
    this.creating.set(true);
    this.error.set('');
    try {
      await this.channelService.createGroup(name, [...this.selectedIds()]);
      this.created.emit();
    } catch (err: any) {
      this.error.set(err?.error?.error ?? 'Failed to create group.');
    } finally {
      this.creating.set(false);
    }
  }

  onCancel(): void {
    this.cancelled.emit();
  }
}
