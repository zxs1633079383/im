import { Component } from '@angular/core';

@Component({
  selector: 'app-channel-list',
  standalone: true,
  imports: [],
  template: `<div class="channel-list-placeholder">Channel List</div>`,
  styles: [`
    .channel-list-placeholder {
      padding: 1rem;
      color: #cdd6f4;
    }
  `],
})
export class ChannelListComponent {}
