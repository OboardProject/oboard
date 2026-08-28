import React from 'react'

export function ServerSettingSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="server-detail-section">
      <h3>{title}</h3>
      {children}
    </section>
  )
}
