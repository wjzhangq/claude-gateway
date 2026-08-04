import { useEffect, useState } from 'react'
import { adminGetConfigLimits, adminUpdateConfigLimits, adminListQuotaOverrides, adminUpsertQuotaOverride, adminDeleteQuotaOverride } from '../api'
import { toast } from '../components/Toast'

interface ProviderRestriction {
  name: string
  allowed_models: string[]
  allowed_itcodes: string[]
}

interface QuotaOverride {
  id: number
  user_id: number
  itcode: string
  name: string
  quota_usd: number
  is_temporary: boolean
  expires_at: string | null
  note: string
  is_expired: boolean
  created_at: string
  updated_at: string
}

interface OverrideForm {
  itcode: string
  quota_usd: string
  is_temporary: boolean
  expires_at: string
  note: string
}

const emptyForm = (): OverrideForm => ({
  itcode: '',
  quota_usd: '',
  is_temporary: false,
  expires_at: '',
  note: '',
})

export default function AdminQuotaPage() {
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [backendMax, setBackendMax] = useState('')
  const [awsDailyMax, setAwsDailyMax] = useState('')
  const [awsMonthlyMax, setAwsMonthlyMax] = useState('')
  const [restrictions, setRestrictions] = useState<ProviderRestriction[]>([])

  const [overrides, setOverrides] = useState<QuotaOverride[]>([])
  const [overridesLoading, setOverridesLoading] = useState(true)
  const [showModal, setShowModal] = useState(false)
  const [editItcode, setEditItcode] = useState<string | null>(null)
  const [form, setForm] = useState<OverrideForm>(emptyForm())
  const [formSaving, setFormSaving] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState<string | null>(null)

  const fetchGlobal = () => {
    setLoading(true)
    adminGetConfigLimits()
      .then((res) => {
        setBackendMax(String(res.data.backend_daily_max))
        setAwsDailyMax(String(res.data.aws_daily_max))
        setAwsMonthlyMax(String(res.data.aws_monthly_max ?? 0))
        setRestrictions(res.data.public_provider_restrictions ?? [])
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }

  const fetchOverrides = () => {
    setOverridesLoading(true)
    adminListQuotaOverrides()
      .then((res) => setOverrides(res.data.overrides || []))
      .catch(() => setOverrides([]))
      .finally(() => setOverridesLoading(false))
  }

  useEffect(() => { fetchGlobal(); fetchOverrides() }, [])

  const saveGlobal = async () => {
    setSaving(true)
    try {
      await adminUpdateConfigLimits({
        backend_daily_max: Number(backendMax),
        aws_daily_max: Number(awsDailyMax),
        aws_monthly_max: Number(awsMonthlyMax),
      })
      toast('全局额度已更新')
      fetchGlobal()
    } catch { /* handled by interceptor */ }
    setSaving(false)
  }

  const openAdd = () => {
    setEditItcode(null)
    setForm(emptyForm())
    setShowModal(true)
  }

  const openEdit = (o: QuotaOverride) => {
    setEditItcode(o.itcode)
    setForm({
      itcode: o.itcode,
      quota_usd: String(o.quota_usd),
      is_temporary: o.is_temporary,
      expires_at: o.expires_at ?? '',
      note: o.note,
    })
    setShowModal(true)
  }

  const handleSubmit = async () => {
    const itcode = editItcode ?? form.itcode.trim()
    if (!itcode) { toast('请输入用户 Itcode'); return }
    const quota = parseFloat(form.quota_usd)
    if (isNaN(quota) || quota < 0) { toast('请输入有效的配额金额'); return }
    if (form.is_temporary && !form.expires_at) { toast('临时额度需要指定到期日期'); return }

    setFormSaving(true)
    try {
      await adminUpsertQuotaOverride(itcode, {
        quota_usd: quota,
        is_temporary: form.is_temporary,
        expires_at: form.is_temporary ? form.expires_at : '',
        note: form.note,
      })
      toast(editItcode ? '额度已更新' : '额度已添加')
      setShowModal(false)
      fetchOverrides()
    } catch { /* handled by interceptor */ }
    setFormSaving(false)
  }

  const handleDelete = async (itcode: string) => {
    try {
      await adminDeleteQuotaOverride(itcode)
      toast('额度覆盖已删除')
      setDeleteConfirm(null)
      fetchOverrides()
    } catch { /* handled by interceptor */ }
  }

  return (
    <div className="p-8">
      <div className="mb-7">
        <h2 className="text-xl font-bold text-gray-900">额度设置</h2>
        <p className="text-sm text-gray-400 mt-0.5">管理全局额度上限和每用户 Backend 额度覆盖</p>
      </div>

      {/* 全局设置 */}
      <div className="bg-white rounded-xl border border-gray-100 shadow-sm p-6 mb-6">
        <h3 className="text-sm font-semibold text-gray-700 uppercase tracking-wide mb-4">全局上限</h3>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
          <div>
            <label className="block text-sm text-gray-500 mb-1">Backend 每日上限 (USD)</label>
            <input
              type="number"
              value={backendMax}
              onChange={(e) => setBackendMax(e.target.value)}
              className="w-full px-3 py-2 border border-gray-200 rounded-lg focus:ring-2 focus:ring-red-500/20 focus:border-red-400 outline-none transition"
              disabled={loading}
            />
          </div>
          <div>
            <label className="block text-sm text-gray-500 mb-1">AWS 每日上限 (USD)</label>
            <input
              type="number"
              value={awsDailyMax}
              onChange={(e) => setAwsDailyMax(e.target.value)}
              className="w-full px-3 py-2 border border-gray-200 rounded-lg focus:ring-2 focus:ring-red-500/20 focus:border-red-400 outline-none transition"
              disabled={loading}
            />
          </div>
          <div>
            <label className="block text-sm text-gray-500 mb-1">
              AWS 每月上限 (USD)
              <span className="ml-1.5 text-xs text-amber-500 font-normal">若 &gt; 0 则启用月计费</span>
            </label>
            <input
              type="number"
              value={awsMonthlyMax}
              onChange={(e) => setAwsMonthlyMax(e.target.value)}
              className="w-full px-3 py-2 border border-gray-200 rounded-lg focus:ring-2 focus:ring-amber-500/20 focus:border-amber-400 outline-none transition"
              disabled={loading}
            />
          </div>
        </div>
        <div className="mt-4 flex justify-end">
          <button
            onClick={saveGlobal}
            disabled={saving || loading}
            className="px-4 py-2 bg-red-600 text-white text-sm font-medium rounded-lg hover:bg-red-700 disabled:opacity-50 transition"
          >
            {saving ? '保存中...' : '保存全局设置'}
          </button>
        </div>
      </div>

      {/* 每用户 Backend 额度覆盖 */}
      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <div>
            <h3 className="text-sm font-semibold text-gray-700 uppercase tracking-wide">每用户 Backend 额度覆盖</h3>
            <p className="text-xs text-gray-400 mt-0.5">优先于全局上限生效。支持永久或临时（指定到期日）。</p>
          </div>
          <button
            onClick={openAdd}
            className="px-3 py-1.5 bg-red-600 text-white text-xs font-medium rounded-lg hover:bg-red-700 transition"
          >
            + 添加覆盖
          </button>
        </div>
        <table className="w-full text-sm">
          <thead>
            <tr className="bg-gray-50/80 text-xs text-gray-500 uppercase tracking-wide">
              <th className="text-left px-6 py-3 font-medium">Itcode</th>
              <th className="text-left px-6 py-3 font-medium">姓名</th>
              <th className="text-right px-6 py-3 font-medium">每日配额 (USD)</th>
              <th className="text-center px-6 py-3 font-medium">类型</th>
              <th className="text-center px-6 py-3 font-medium">到期日期</th>
              <th className="text-center px-6 py-3 font-medium">状态</th>
              <th className="text-left px-6 py-3 font-medium">备注</th>
              <th className="text-center px-6 py-3 font-medium">操作</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {overridesLoading ? (
              Array.from({ length: 3 }).map((_, i) => (
                <tr key={i}>
                  {[90, 70, 70, 60, 80, 60, 100, 80].map((w, j) => (
                    <td key={j} className="px-6 py-3.5">
                      <div className="skeleton h-3.5 rounded" style={{ width: w }} />
                    </td>
                  ))}
                </tr>
              ))
            ) : overrides.length === 0 ? (
              <tr>
                <td colSpan={8} className="text-center py-8 text-gray-400 text-sm">
                  暂无额度覆盖配置
                </td>
              </tr>
            ) : (
              overrides.map((o) => (
                <tr key={o.itcode} className={`hover:bg-gray-50/50 transition-colors${o.is_expired ? ' opacity-50' : ''}`}>
                  <td className="px-6 py-3.5 font-medium text-gray-900">{o.itcode}</td>
                  <td className="px-6 py-3.5 text-gray-500">{o.name || '—'}</td>
                  <td className="px-6 py-3.5 text-right font-medium text-gray-800">${o.quota_usd.toFixed(2)}</td>
                  <td className="px-6 py-3.5 text-center">
                    <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${
                      o.is_temporary
                        ? 'bg-amber-50 text-amber-700 ring-amber-100'
                        : 'bg-blue-50 text-blue-700 ring-blue-100'
                    }`}>
                      {o.is_temporary ? '临时' : '永久'}
                    </span>
                  </td>
                  <td className="px-6 py-3.5 text-center text-gray-500 text-xs">
                    {o.expires_at ?? '—'}
                  </td>
                  <td className="px-6 py-3.5 text-center">
                    <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${
                      o.is_expired
                        ? 'bg-gray-100 text-gray-500 ring-gray-200'
                        : 'bg-green-50 text-green-700 ring-green-100'
                    }`}>
                      {o.is_expired ? '已过期' : '有效'}
                    </span>
                  </td>
                  <td className="px-6 py-3.5 text-gray-400 text-xs max-w-[160px] truncate" title={o.note}>{o.note || '—'}</td>
                  <td className="px-6 py-3.5 text-center">
                    <div className="flex items-center justify-center gap-2">
                      <button
                        onClick={() => openEdit(o)}
                        className="text-xs text-red-600 hover:text-red-800 transition"
                      >
                        编辑
                      </button>
                      {deleteConfirm === o.itcode ? (
                        <span className="flex items-center gap-1 text-xs">
                          <button onClick={() => handleDelete(o.itcode)} className="text-red-600 hover:text-red-800 font-medium">确认</button>
                          <button onClick={() => setDeleteConfirm(null)} className="text-gray-400 hover:text-gray-600">取消</button>
                        </span>
                      ) : (
                        <button
                          onClick={() => setDeleteConfirm(o.itcode)}
                          className="text-xs text-gray-400 hover:text-red-600 transition"
                        >
                          删除
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      {/* 模型访问限制（来自 config.yaml） */}
      {restrictions.length > 0 && (
        <div className="mt-6 bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
          <div className="px-6 py-4 border-b border-gray-100">
            <h3 className="text-sm font-semibold text-gray-700 uppercase tracking-wide">模型访问限制</h3>
            <p className="text-xs text-gray-400 mt-0.5">来自 config.yaml，仅特定用户可访问的 Public Provider 模型</p>
          </div>
          <div className="divide-y divide-gray-50">
            {restrictions.map((r) => (
              <div key={r.name} className="px-6 py-4">
                <div className="flex items-center gap-2 mb-3">
                  <span className="text-sm font-semibold text-gray-800">{r.name}</span>
                  {r.allowed_models.length > 0 && r.allowed_models.map((m) => (
                    <span key={m} className="inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium bg-purple-50 text-purple-700 ring-1 ring-purple-100">
                      {m}
                    </span>
                  ))}
                </div>
                <div>
                  <p className="text-xs text-gray-400 mb-2">可访问用户（{r.allowed_itcodes.length} 人）</p>
                  <div className="flex flex-wrap gap-1.5">
                    {r.allowed_itcodes.map((itcode) => (
                      <span key={itcode} className="inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium bg-gray-100 text-gray-700">
                        {itcode}
                      </span>
                    ))}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 新增/编辑弹窗 */}
      {showModal && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 backdrop-blur-sm" onClick={() => setShowModal(false)}>
          <div className="bg-white rounded-2xl shadow-2xl w-[460px] p-6 border border-gray-100" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="text-base font-bold text-gray-900">{editItcode ? '编辑额度覆盖' : '添加额度覆盖'}</h3>
              <button onClick={() => setShowModal(false)} className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition-colors text-sm">✕</button>
            </div>
            <div className="space-y-4">
              <div>
                <label className="block text-sm text-gray-600 mb-1.5 font-medium">用户 Itcode</label>
                <input
                  value={form.itcode}
                  onChange={(e) => setForm((f) => ({ ...f, itcode: e.target.value }))}
                  disabled={editItcode !== null}
                  placeholder="输入用户 itcode"
                  className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-red-500/20 focus:border-red-400 outline-none transition disabled:bg-gray-50 disabled:text-gray-400"
                />
              </div>
              <div>
                <label className="block text-sm text-gray-600 mb-1.5 font-medium">每日配额 (USD)</label>
                <input
                  type="number"
                  min="0"
                  step="50"
                  value={form.quota_usd}
                  onChange={(e) => setForm((f) => ({ ...f, quota_usd: e.target.value }))}
                  placeholder="例如 600"
                  className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-red-500/20 focus:border-red-400 outline-none transition"
                />
              </div>
              <div>
                <label className="block text-sm text-gray-600 mb-2 font-medium">配额类型</label>
                <div className="flex gap-4">
                  <label className="flex items-center gap-2 cursor-pointer">
                    <input
                      type="radio"
                      checked={!form.is_temporary}
                      onChange={() => setForm((f) => ({ ...f, is_temporary: false, expires_at: '' }))}
                      className="accent-red-600"
                    />
                    <span className="text-sm text-gray-700">永久</span>
                  </label>
                  <label className="flex items-center gap-2 cursor-pointer">
                    <input
                      type="radio"
                      checked={form.is_temporary}
                      onChange={() => setForm((f) => ({ ...f, is_temporary: true }))}
                      className="accent-red-600"
                    />
                    <span className="text-sm text-gray-700">临时（有到期时间）</span>
                  </label>
                </div>
              </div>
              {form.is_temporary && (
                <div>
                  <label className="block text-sm text-gray-600 mb-1.5 font-medium">到期日期</label>
                  <input
                    type="date"
                    value={form.expires_at}
                    onChange={(e) => setForm((f) => ({ ...f, expires_at: e.target.value }))}
                    className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-red-500/20 focus:border-red-400 outline-none transition"
                  />
                  <p className="text-xs text-gray-400 mt-1">到期后该用户恢复默认全局配额</p>
                </div>
              )}
              <div>
                <label className="block text-sm text-gray-600 mb-1.5 font-medium">备注 <span className="text-gray-400 font-normal">（可选）</span></label>
                <textarea
                  value={form.note}
                  onChange={(e) => setForm((f) => ({ ...f, note: e.target.value }))}
                  rows={2}
                  maxLength={200}
                  placeholder="添加备注说明"
                  className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-red-500/20 focus:border-red-400 outline-none transition resize-none"
                />
              </div>
            </div>
            <div className="mt-5 flex justify-end gap-2">
              <button
                onClick={() => setShowModal(false)}
                className="px-4 py-2 text-sm border border-gray-200 rounded-lg hover:bg-gray-100 transition"
              >
                取消
              </button>
              <button
                onClick={handleSubmit}
                disabled={formSaving}
                className="px-4 py-2 bg-red-600 text-white text-sm font-medium rounded-lg hover:bg-red-700 disabled:opacity-50 transition"
              >
                {formSaving ? '保存中...' : '确认'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
