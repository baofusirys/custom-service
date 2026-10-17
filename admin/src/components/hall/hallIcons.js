// 大厅页使用的线性 SVG 图标（24×24 描边、currentColor），以组件形式交给 el-button 的 icon 属性或 el-icon 渲染。
// 严禁 Emoji；图形为自绘几何路径，无第三方图标库依赖。
import { defineComponent, h, markRaw } from 'vue'

const CIRCLE = (cx, cy, r) => 'M' + (cx - r) + ' ' + cy + 'a' + r + ' ' + r + ' 0 1 0 ' + 2 * r + ' 0a' + r + ' ' + r + ' 0 1 0 ' + -2 * r + ' 0z'
const MIC = ['M9 6a3 3 0 0 1 6 0v5a3 3 0 0 1-6 0z', 'M5 11a7 7 0 0 0 14 0', 'M12 18v3', 'M9 21h6']
const CAMERA = ['M4 7h9a2 2 0 0 1 2 2v6a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V9a2 2 0 0 1 2-2z', 'M15 10.5l5-3v9l-5-3']
const USER = [CIRCLE(9, 7.5, 3.5), 'M2.5 20a6.5 6.5 0 0 1 13 0']
const SLASH = 'M3 3l18 18'

export const HALL_ICON_PATHS = Object.freeze({
  users: [...USER, 'M16 4.5a3.5 3.5 0 0 1 0 6.5', 'M21.5 20a6.5 6.5 0 0 0-4-6'],
  mic: MIC,
  micOff: [...MIC, SLASH],
  camera: CAMERA,
  cameraOff: [...CAMERA, SLASH],
  volumeOff: ['M4 9h3l5-4v14l-5-4H4z', 'M16 9l5 6', 'M21 9l-5 6'],
  enter: ['M14 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4', 'M10 17l5-5-5-5', 'M15 12H3'],
  leave: ['M10 21H6a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4', 'M16 17l5-5-5-5', 'M21 12H9'],
  refresh: ['M20 12a8 8 0 1 1-2.34-5.66', 'M20 4v5h-5'],
  image: ['M5 4h14a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2z', CIRCLE(9, 9, 2), 'M21 16l-5-5-10 9'],
  attach: ['M21 11.5l-8.6 8.6a5.5 5.5 0 0 1-7.8-7.8l8.6-8.6a3.7 3.7 0 0 1 5.2 5.2l-8.6 8.6a1.8 1.8 0 0 1-2.6-2.6l7.9-7.9'],
  send: ['M22 2L11 13', 'M22 2l-7 20-4-9-9-4z'],
  trash: ['M3 6h18', 'M8 6V4h8v2', 'M6 6l1 14h10l1-14', 'M10 11v6', 'M14 11v6'],
  kick: [...USER, 'M17 8l5 5', 'M22 8l-5 5'],
  ban: [CIRCLE(12, 12, 9), 'M5.6 5.6l12.8 12.8'],
  chat: ['M21 12a8 8 0 0 1-11.7 7.1L4 20l1.1-4.6A8 8 0 1 1 21 12z'],
  speaking: ['M4 10v4', 'M8 7v10', 'M12 4v16', 'M16 7v10', 'M20 10v4'],
  volume: ['M4 9h3l5-4v14l-5-4H4z', 'M16 9a4 4 0 0 1 0 6', 'M19 6a8 8 0 0 1 0 12'],
})

function makeIcon(name, paths) {
  return markRaw(defineComponent({
    name: 'HallIcon' + name.charAt(0).toUpperCase() + name.slice(1),
    render() {
      return h('svg', {
        viewBox: '0 0 24 24',
        fill: 'none',
        stroke: 'currentColor',
        'stroke-width': '2',
        'stroke-linecap': 'round',
        'stroke-linejoin': 'round',
        'aria-hidden': 'true',
        focusable: 'false',
      }, paths.map(d => h('path', { d })))
    },
  }))
}

export const hallIcons = Object.freeze(Object.fromEntries(
  Object.entries(HALL_ICON_PATHS).map(([name, paths]) => [name, makeIcon(name, paths)]),
))
