import type { ControllerBackup, ControllerBackupSettings } from './types'

export const emptySettings: ControllerBackupSettings = {
    enabled: false, schedule: 'daily', time: '03:00', weekday: 0, local_retention: 7, remote_retention: 30, update_retention: 2,
    destination: { provider: '', endpoint: '', bucket: '', prefix: '', region: 'us-east-1', force_path_style: false, enabled: false },
    password_configured: false, destination_configured: true,
  }

export const backupStatus = (item: ControllerBackup) => item.local_status === 'pending'
    ? '备份创建中'
    : item.remote_status === 'failed'
      ? (item.local_status === 'available' ? '本地可用，远端失败' : '副本不可用')
      : item.local_status === 'available' && item.remote_status === 'available' ? '本地和远端可用'
        : item.local_status === 'available' ? '本地可用'
          : item.remote_retrievable ? '可从第三方取回'
            : item.remote_status === 'available' ? '保留在旧目标' : '副本不可用'
