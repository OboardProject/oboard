import React from 'react'

const qureRegionFlags: Record<string, string> = {
  AR: 'Argentina.png', AU: 'Australia.png', BR: 'Brazil.png', CA: 'Canada.png', CN: 'China.png',
  DE: 'Germany.png', EG: 'Egypt.png', FI: 'Finland.png', FR: 'France.png', GB: 'United_Kingdom.png',
  HK: 'Hong_Kong.png', IN: 'India.png', JP: 'Japan.png', KR: 'Korea.png', MO: 'Macao.png',
  MY: 'Malaysia.png', PH: 'Philippines.png', RU: 'Russia.png', SG: 'Singapore.png', TH: 'Thailand.png',
  TR: 'Turkey.png', TW: 'Taiwan.png', UA: 'Ukraine.png', US: 'United_States.png',
}

const isoRegionCodes = new Set('AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CP CR CU CV CW CX CY CZ DE DG DJ DK DM DO DZ EC EE EG EH ER ES ET EU FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU IC ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PC PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM UN US UY UZ VA VC VE VG VI VN VU WF WS XK XX YE YT ZA ZM ZW'.split(' '))

const regionLabelOverrides: Record<string, string> = {
  CN: '中国', HK: '香港', MO: '澳门', TW: '台湾',
}

const regionDisplayNames = typeof (Intl as any).DisplayNames === 'function'
  ? new (Intl as any).DisplayNames(['zh-CN'], { type: 'region' })
  : null

export function normalizeRegionCode(code?: string) {
  const value = String(code || '').trim().toUpperCase()
  return /^[A-Z]{2}$/.test(value) ? value : ''
}

export function regionLabel(code?: string) {
  const value = normalizeRegionCode(code)
  if (!value) return '未知地区'
  if (regionLabelOverrides[value]) return regionLabelOverrides[value]
  const localized = regionDisplayNames?.of(value)
  return localized && localized !== value ? localized : value
}

export function regionFlagEmoji(code: string) {
  return Array.from(code).map(char => String.fromCodePoint(127397 + char.charCodeAt(0))).join('')
}

const appBasePath = (() => {
  if (typeof document === 'undefined') return ''
  const href = document.querySelector('base')?.getAttribute('href') || '/'
  const pathname = new URL(href, window.location.origin).pathname.replace(/\/+$/, '')
  return pathname === '/' ? '' : pathname
})()

export function appPath(path: string) {
  const suffix = path.startsWith('/') ? path : `/${path}`
  return `${appBasePath}${suffix}` || '/'
}

export function serverRegionCode(server?: { region_mode?: string; region_code?: string; detected_region_code?: string } | null) {
  if (!server) return ''
  const raw = server.region_mode === 'manual' ? server.region_code : (server.detected_region_code || server.region_code)
  return normalizeRegionCode(raw)
}

export function RegionFlag({
  code,
  size = 18,
  className,
  fallback,
}: {
  code?: string
  size?: number
  className?: string
  fallback?: React.ReactNode
}) {
  const value = normalizeRegionCode(code)
  if (!value && fallback !== undefined) {
    return <>{fallback}</>
  }

  const label = regionLabel(value)
  const qureFilename = value ? qureRegionFlags[value] : 'World_Map.png'
  const classes = className ? `region-flag ${className}` : 'region-flag'

  if (qureFilename) {
    return (
      <img
        className={classes}
        src={appPath(`/region-flags/${qureFilename}`)}
        width={size}
        height={size}
        alt={label}
        title={label}
      />
    )
  }
  if (isoRegionCodes.has(value)) {
    return (
      <img
        className={classes}
        src={appPath(`/region-flags/iso/${value.toLowerCase()}.svg`)}
        width={size}
        height={size}
        alt={label}
        title={label}
      />
    )
  }
  if (value) {
    return (
      <span
        className={`${classes} region-flag-emoji`}
        style={{ width: size, height: size, fontSize: Math.max(13, size * 0.72) }}
        role="img"
        aria-label={label}
        title={label}
      >
        {regionFlagEmoji(value)}
      </span>
    )
  }
  return (
    <span
      className={`${classes} region-flag-emoji`}
      style={{ width: size, height: size, fontSize: Math.max(13, size * 0.72) }}
      role="img"
      aria-label="未知地区"
      title="未知地区"
    >
      🌐
    </span>
  )
}
