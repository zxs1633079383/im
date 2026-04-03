import { Component, inject, OnInit } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { RouterOutlet } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { ChannelListComponent } from '../channel-list/channel-list.component';
import { ChannelService } from '../../core/channels/channel.service';
import { FriendService } from '../../core/friends/friend.service';
import { ThemeService, Theme } from '../../core/theme/theme.service';
import { I18nService } from '../../core/i18n/i18n.service';
import { API_BASE } from '../../core/config/api.config';

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
  private themeService = inject(ThemeService);
  private i18n = inject(I18nService);
  private http = inject(HttpClient);

  async ngOnInit(): Promise<void> {
    await Promise.all([
      this.channelService.loadChannels(),
      this.friendService.loadFriends(),
      this.loadAndApplySettings(),
    ]);
  }

  private async loadAndApplySettings(): Promise<void> {
    try {
      const s = await firstValueFrom(
        this.http.get<{ theme: string; language: string }>(`${API_BASE}/settings`)
      );
      this.themeService.applyTheme((s.theme || 'system') as Theme);
      this.i18n.setLocale(s.language || 'zh');
    } catch {
      // First login, no settings yet — defaults already applied
    }
  }
}
