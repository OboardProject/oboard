import * as React from 'react'

export type DialogTone = 'default' | 'danger'
export type DialogChoice = { value: string; label: string }
export type DialogBase = {
  title: string
  message?: React.ReactNode
  confirmText?: string
  cancelText?: string
  tone?: DialogTone
}
export type PromptDialogOptions = DialogBase & {
  defaultValue?: string
  placeholder?: string
  inputType?: string
  choices?: DialogChoice[]
}
export type DialogState =
  | ({ id: number; kind: 'alert'; resolve: () => void } & DialogBase)
  | ({ id: number; kind: 'confirm'; resolve: (value: boolean) => void } & DialogBase)
  | ({ id: number; kind: 'prompt'; resolve: (value: string | null) => void } & PromptDialogOptions)

export type DialogApi = {
  alert: (options: DialogBase) => Promise<void>
  confirm: (options: DialogBase) => Promise<boolean>
  prompt: (options: PromptDialogOptions) => Promise<string | null>
}

export const DialogContext = React.createContext<DialogApi | null>(null)

export function useDialogs() {
  const dialogs = React.useContext(DialogContext)
  if (!dialogs) throw new Error('DialogContext is not mounted')
  return dialogs
}
