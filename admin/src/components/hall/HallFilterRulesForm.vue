<script setup>
// [122] 多人大厅 → 敏感词屏蔽规则（契约第 9.1/9.2 节）：启用开关、处理方式、拦截类别、
// 自定义屏蔽词 / 放行词 / 放行域名（一行一个，前端预校验，以服务端为准）、内置词库只读展示、检测试用。
// 加载失败时禁止保存，避免用空表单覆盖服务端已有的词表。
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getHallFilter, saveHallFilter } from '../../api/hallFilter'
import { hallErrorMessage } from '../../modules/hallErrors'
import {
  BUILTIN_GROUPS,
  DEFAULT_FILTER_LIMITS,
  FILTER_CATEGORIES,
  FILTER_MODES,
  buildFilterPayload,
  checkFilterLists,
  emptyFilterForm,
  filterSnapshot,
  parseFilterConfig,
} from '../../modules/hallFilterForm'
import HallFilterTester from './HallFilterTester.vue'

const form = reactive(emptyFilterForm())
const limits = ref({ ...DEFAULT_FILTER_LIMITS })
const builtin = ref(Object.fromEntries(BUILTIN_GROUPS.map(g => [g.key, []])))
const loading = ref(false)
const saving = ref(false)
const loaded = ref(false)
const loadError = ref('')
const savedSnapshot = ref('')

const lists = computed(() => checkFilterLists(form, limits.value))
// 加载完成前不显示词表错误
const listErrors = computed(() => loaded.value
  ? { custom: lists.value.custom.error, allow: lists.value.allow.error, domains: lists.value.domains.error }
  : { custom: '', allow: '', domains: '' })
const dirty = computed(() => loaded.value && filterSnapshot(form) !== savedSnapshot.value)

function countText(result, max) {
  let text = '当前 ' + result.count + ' / ' + max + ' 条'
  if (result.duplicates) text += '（另有 ' + result.duplicates + ' 条重复，保存时自动去掉）'
  return text
}

function apply(data) {
  const parsed = parseFilterConfig(data)
  Object.assign(form, parsed.form)
  limits.value = parsed.limits
  builtin.value = parsed.builtin
  savedSnapshot.value = filterSnapshot(form)
  loaded.value = true
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const r = await getHallFilter()
    apply(r?.data)
  } catch (e) {
    loadError.value = hallErrorMessage(e, '加载敏感词设置失败，请稍后重试')
  } finally {
    loading.value = false
  }
}

async function reload() {
  if (dirty.value) {
    try {
      await ElMessageBox.confirm('当前有未保存的修改，重新加载会丢弃这些修改，确定继续？', '重新加载', {
        type: 'warning', confirmButtonText: '丢弃并重新加载', cancelButtonText: '取消',
      })
    } catch {
      return
    }
  }
  await load()
}

async function save() {
  if (!loaded.value || saving.value) return
  const built = buildFilterPayload(form, limits.value)
  if (!built.ok) {
    ElMessage.warning(built.error)
    return
  }
  saving.value = true
  try {
    const r = await saveHallFilter(built.body)
    // 以服务端规范化后的结果为准（去重、域名转小写 / punycode）；响应缺少 data 时回退为本次提交的内容
    apply(r?.data && typeof r.data === 'object'
      ? r.data
      : { ...built.body, limits: limits.value, builtin: builtin.value })
    ElMessage.success('敏感词设置已保存')
  } catch (e) {
    ElMessage.error(hallErrorMessage(e, '保存敏感词设置失败，请稍后重试'))
  } finally {
    saving.value = false
  }
}

// 检测试用按当前页面上的规则（含未保存的修改）预览
function currentConfig() {
  return buildFilterPayload(form, limits.value)
}

onMounted(load)
</script>

<template>
  <el-alert v-if="loadError" type="error" show-icon :closable="false" :title="loadError">
    <el-button size="small" :loading="loading" @click="load">重新加载</el-button>
  </el-alert>

  <el-form v-loading="loading" label-width="120px" label-position="left" @submit.prevent>
    <el-form-item label="启用屏蔽">
      <el-switch v-model="form.enabled" active-text="开启" inactive-text="关闭" data-testid="hall-filter-enabled" />
    </el-form-item>

    <el-form-item label="处理方式">
      <el-radio-group v-model="form.mode" data-testid="hall-filter-mode">
        <el-radio v-for="m in FILTER_MODES" :key="m.value" :value="m.value">{{ m.label }}</el-radio>
      </el-radio-group>
    </el-form-item>

    <el-form-item label="拦截类别">
      <el-space direction="vertical" alignment="flex-start" :size="4">
        <el-checkbox-group v-model="form.categories" data-testid="hall-filter-categories">
          <el-checkbox
            v-for="c in FILTER_CATEGORIES"
            :key="c.value"
            :value="c.value"
            :data-testid="'hall-filter-cat-' + c.value">{{ c.label }}</el-checkbox>
        </el-checkbox-group>
        <el-text size="small" type="info">自定义屏蔽词始终生效，不受类别勾选影响。</el-text>
        <el-text size="small" type="info">超链接含「点com」等变体（放行域名除外）；联系方式含手机号、9 位以上数字、QQ / 微信 / Telegram 等后面跟账号、邮箱、@用户名。</el-text>
      </el-space>
    </el-form-item>

    <el-form-item label="自定义屏蔽词" :error="listErrors.custom">
      <el-input
        v-model="form.customWordsText"
        type="textarea"
        :autosize="{ minRows: 3, maxRows: 10 }"
        placeholder="一行一个，命中即替换为 **（始终生效）"
        data-testid="hall-filter-custom-words" />
      <el-text size="small" :type="lists.custom.count > limits.custom_words ? 'danger' : 'info'" data-testid="hall-filter-custom-count">
        {{ countText(lists.custom, limits.custom_words) }}；每个最多 {{ limits.word_max_runes }} 个字
      </el-text>
    </el-form-item>

    <el-form-item label="放行词" :error="listErrors.allow">
      <el-input
        v-model="form.allowWordsText"
        type="textarea"
        :autosize="{ minRows: 2, maxRows: 8 }"
        placeholder="一行一个，放行词所在范围内的命中不屏蔽（用于处理误拦）"
        data-testid="hall-filter-allow-words" />
      <el-text size="small" :type="lists.allow.count > limits.allow_words ? 'danger' : 'info'" data-testid="hall-filter-allow-count">
        {{ countText(lists.allow, limits.allow_words) }}；每个最多 {{ limits.word_max_runes }} 个字
      </el-text>
    </el-form-item>

    <el-form-item label="放行域名" :error="listErrors.domains">
      <el-input
        v-model="form.allowDomainsText"
        type="textarea"
        :autosize="{ minRows: 2, maxRows: 8 }"
        placeholder="一行一个，只填域名，这些网址不按超链接屏蔽"
        data-testid="hall-filter-allow-domains" />
      <el-text size="small" :type="lists.domains.count > limits.allow_domains ? 'danger' : 'info'" data-testid="hall-filter-domain-count">
        {{ countText(lists.domains, limits.allow_domains) }}；保存时自动转小写，中文域名由服务器转换
      </el-text>
    </el-form-item>

    <el-form-item>
      <el-space wrap>
        <el-button type="primary" :loading="saving" :disabled="!loaded" data-testid="hall-filter-save" @click="save">保存屏蔽设置</el-button>
        <el-button :disabled="loading || saving" @click="reload">重新加载</el-button>
        <el-tag v-if="dirty" type="warning" effect="plain" data-testid="hall-filter-dirty">有未保存的修改</el-tag>
      </el-space>
    </el-form-item>
  </el-form>

  <el-divider content-position="left">内置词库（只读）</el-divider>
  <el-collapse data-testid="hall-filter-builtin">
    <el-collapse-item
      v-for="g in BUILTIN_GROUPS"
      :key="g.key"
      :name="g.key"
      :title="g.label + '（' + builtin[g.key].length + ' 个）'">
      <el-text v-if="builtin[g.key].length" size="small">{{ builtin[g.key].join('、') }}</el-text>
      <el-text v-else size="small" type="info">暂无</el-text>
    </el-collapse-item>
  </el-collapse>
  <el-text size="small" type="info">内置词库随系统版本更新，不能在这里修改；需要补充请填「自定义屏蔽词」，误拦请填「放行词」。超链接与手机号、账号等联系方式按规则识别，不依赖词库。</el-text>

  <el-divider content-position="left">检测试用</el-divider>
  <HallFilterTester :build-config="currentConfig" :disabled="!loaded" />
</template>
