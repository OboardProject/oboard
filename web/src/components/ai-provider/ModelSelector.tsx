import { RefreshCw } from 'lucide-react'
import { SearchableCombobox } from '../ui/SearchableCombobox'

export function ModelSelector({ value, options, loading, status, disabled, onChange, onDiscover }: { value: string; options: string[]; loading: boolean; status?: string; disabled?: boolean; onChange: (value: string) => void; onDiscover: () => void }) {
  return <>
    <div className="ai-provider-model-control">
      <SearchableCombobox required ariaLabel="模型" value={value} options={options} placeholder="输入或选择模型 ID" onChange={onChange} />
      <button type="button" className="ghost icon-button" disabled={disabled || loading} onClick={onDiscover} title="拉取可用模型" aria-label="拉取可用模型"><RefreshCw size={16} className={loading ? 'spin' : ''} /></button>
    </div>
    {status ? <small className="ai-provider-model-status">{status}</small> : null}
  </>
}
