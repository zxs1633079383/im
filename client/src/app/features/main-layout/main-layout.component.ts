import { Component, inject, OnInit } from '@angular/core';
import { RouterOutlet } from '@angular/router';
import { ChannelListComponent } from '../channel-list/channel-list.component';
import { ChannelService } from '../../core/channels/channel.service';
import { FriendService } from '../../core/friends/friend.service';

@Component({
  selector: 'app-main-layout',
  standalone: true,
  imports: [RouterOutlet, ChannelListComponent],
  templateUrl: './main-layout.component.html',
  styleUrl: './main-layout.component.scss',
})
export class MainLayoutComponent implements OnInit {
  private channelService = inject(ChannelService);
  private friendService = inject(FriendService);

  async ngOnInit(): Promise<void> {
    await Promise.all([
      this.channelService.loadChannels(),
      this.friendService.loadFriends(),
    ]);
  }
}
