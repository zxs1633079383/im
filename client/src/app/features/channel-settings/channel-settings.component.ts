import { Component } from '@angular/core';

@Component({
  selector: 'app-channel-settings',
  standalone: true,
  imports: [],
  template: `<div class="channel-settings-placeholder">Channel Settings</div>`,
  styles: [`
    .channel-settings-placeholder {
      padding: 2rem;
      color: #cdd6f4;
    }
  `],
})
export class ChannelSettingsComponent {}
