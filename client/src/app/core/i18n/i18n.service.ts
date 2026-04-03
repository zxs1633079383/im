import { Injectable, signal } from '@angular/core';

export type Locale = 'en' | 'zh' | 'ja' | 'ko';

const STORAGE_KEY = 'app_locale';

const translations: Record<string, Record<string, string>> = {
  en: {
    'nav.contacts': 'Contacts',
    'nav.search': 'Search',
    'nav.profile': 'Profile',
    'nav.settings': 'Settings',
    'nav.signout': 'Sign out',
    'nav.chats': 'Chats',
    'nav.newGroup': 'New Group',
    'chat.placeholder': 'Type a message...',
    'chat.send': 'Send',
    'chat.noMessages': 'No messages yet',
    'settings.title': 'Settings',
    'settings.notifications': 'Notifications',
    'settings.enableNotif': 'Enable Notifications',
    'settings.notifDesc': 'Receive desktop notifications for new messages.',
    'settings.appearance': 'Appearance',
    'settings.theme': 'Theme',
    'settings.themeDesc': 'Choose your preferred color theme.',
    'settings.language': 'Language',
    'settings.langLabel': 'Display Language',
    'settings.langDesc': 'Set the language for the application interface.',
    'settings.save': 'Save Settings',
    'settings.saving': 'Saving...',
    'settings.saved': 'Settings saved!',
    'common.loading': 'Loading...',
    'common.error': 'An error occurred.',
  },
  zh: {
    'nav.contacts': '联系人',
    'nav.search': '搜索',
    'nav.profile': '个人资料',
    'nav.settings': '设置',
    'nav.signout': '退出登录',
    'nav.chats': '聊天',
    'nav.newGroup': '新建群组',
    'chat.placeholder': '输入消息...',
    'chat.send': '发送',
    'chat.noMessages': '暂无消息',
    'settings.title': '设置',
    'settings.notifications': '通知',
    'settings.enableNotif': '启用通知',
    'settings.notifDesc': '接收新消息的桌面通知。',
    'settings.appearance': '外观',
    'settings.theme': '主题',
    'settings.themeDesc': '选择你喜欢的主题颜色。',
    'settings.language': '语言',
    'settings.langLabel': '显示语言',
    'settings.langDesc': '设置应用界面的显示语言。',
    'settings.save': '保存设置',
    'settings.saving': '保存中...',
    'settings.saved': '设置已保存！',
    'common.loading': '加载中...',
    'common.error': '发生错误。',
  },
  ja: {
    'nav.contacts': '連絡先',
    'nav.search': '検索',
    'nav.profile': 'プロフィール',
    'nav.settings': '設定',
    'nav.signout': 'サインアウト',
    'nav.chats': 'チャット',
    'nav.newGroup': '新しいグループ',
    'chat.placeholder': 'メッセージを入力...',
    'chat.send': '送信',
    'chat.noMessages': 'メッセージなし',
    'settings.title': '設定',
    'settings.notifications': '通知',
    'settings.enableNotif': '通知を有効にする',
    'settings.notifDesc': '新しいメッセージのデスクトップ通知を受け取ります。',
    'settings.appearance': '外観',
    'settings.theme': 'テーマ',
    'settings.themeDesc': '好みのカラーテーマを選択してください。',
    'settings.language': '言語',
    'settings.langLabel': '表示言語',
    'settings.langDesc': 'アプリケーションインターフェースの言語を設定します。',
    'settings.save': '設定を保存',
    'settings.saving': '保存中...',
    'settings.saved': '設定が保存されました！',
    'common.loading': '読み込み中...',
    'common.error': 'エラーが発生しました。',
  },
  ko: {
    'nav.contacts': '연락처',
    'nav.search': '검색',
    'nav.profile': '프로필',
    'nav.settings': '설정',
    'nav.signout': '로그아웃',
    'nav.chats': '채팅',
    'nav.newGroup': '새 그룹',
    'chat.placeholder': '메시지 입력...',
    'chat.send': '보내기',
    'chat.noMessages': '메시지가 없습니다',
    'settings.title': '설정',
    'settings.notifications': '알림',
    'settings.enableNotif': '알림 활성화',
    'settings.notifDesc': '새 메시지에 대한 데스크톱 알림을 받습니다.',
    'settings.appearance': '외관',
    'settings.theme': '테마',
    'settings.themeDesc': '원하는 색상 테마를 선택하세요.',
    'settings.language': '언어',
    'settings.langLabel': '표시 언어',
    'settings.langDesc': '애플리케이션 인터페이스의 언어를 설정합니다.',
    'settings.save': '설정 저장',
    'settings.saving': '저장 중...',
    'settings.saved': '설정이 저장되었습니다!',
    'common.loading': '로딩 중...',
    'common.error': '오류가 발생했습니다.',
  },
};

@Injectable({ providedIn: 'root' })
export class I18nService {
  locale = signal<Locale>((localStorage.getItem(STORAGE_KEY) as Locale) || 'zh');

  t(key: string): string {
    const lang = this.locale();
    const map = translations[lang] ?? translations['en'];
    return map[key] ?? translations['en'][key] ?? key;
  }

  setLocale(locale: string): void {
    const valid: Locale[] = ['en', 'zh', 'ja', 'ko'];
    const l = valid.includes(locale as Locale) ? (locale as Locale) : 'en';
    this.locale.set(l);
    localStorage.setItem(STORAGE_KEY, l);
  }
}
