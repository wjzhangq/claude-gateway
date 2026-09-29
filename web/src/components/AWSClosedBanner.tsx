import { useNavigate } from 'react-router-dom'

interface AWSClosedBannerProps {
  /** Optional custom message from the backend (aws_closed_message). Falls back to a built-in default. */
  message?: string
  /** When true the "切换到 Backend 渠道" action button is shown (set to false on AWSKeysPage where the button is already present in the table). */
  showSwitchButton?: boolean
}

const DEFAULT_MESSAGE =
  'AWS Bedrock 渠道已关闭，现有 AWS Key 将无法继续使用，请将 Key 切换回 Backend 渠道后重试。'

/**
 * Red dismissal-less banner shown when aws.channel_closed is true.
 * Used by AWSPage, AWSKeysPage, and AWSUsagePage.
 */
export default function AWSClosedBanner({ message, showSwitchButton = true }: AWSClosedBannerProps) {
  const navigate = useNavigate()
  const text = message || DEFAULT_MESSAGE

  return (
    <div className="mb-5 px-4 py-3 bg-red-50 border border-red-200 rounded-xl flex items-start gap-2.5">
      <span className="text-red-500 mt-0.5 flex-shrink-0 text-base">&#9888;</span>
      <div className="flex-1 text-sm text-red-800">
        <span className="font-semibold">AWS 渠道已关闭：</span>
        {text}
        {showSwitchButton && (
          <button
            onClick={() => navigate('/aws/keys')}
            className="ml-2 inline-flex items-center px-2.5 py-1 rounded-md text-xs font-semibold bg-red-100 text-red-700 hover:bg-red-200 transition-colors"
          >
            前往切换 Backend 渠道 →
          </button>
        )}
      </div>
    </div>
  )
}
