// Pinia store：节点与选中态的单一数据源。
// 从 v2 nodesStore.ts 迁移，结构保持一致。
// 设计见 ../../../design-v3.md §5.2。

import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { ApiNode } from '@/protocol/types'
import { generateNodeId } from '@/protocol/types'

export const useNodesStore = defineStore('nodes', () => {
  const nodes = ref<Record<string, ApiNode>>({})
  const selectedNodes = ref<string[]>([])
  const currentXtremNode = ref<ApiNode | null>(null)

  const allNodes = computed(() => Object.values(nodes.value))
  const getSelectedNodes = computed(() =>
    selectedNodes.value.map((id) => nodes.value[id]).filter(Boolean),
  )

  function setNode(node: ApiNode): void {
    nodes.value[generateNodeId(node)] = node
  }
  function getNodeById(id: string): ApiNode | undefined {
    return nodes.value[id]
  }
  function removeNode(id: string): void {
    delete nodes.value[id]
    removeFromSelectedNodes(id)
  }
  function clearNodes(): void {
    nodes.value = {}
    selectedNodes.value = []
  }
  function setAllNodes(list: ApiNode[]): void {
    list.forEach(setNode)
  }

  function addToSelectedNodes(id: string): void {
    if (!selectedNodes.value.includes(id)) selectedNodes.value.push(id)
  }
  function removeFromSelectedNodes(id: string): void {
    selectedNodes.value = selectedNodes.value.filter((n) => n !== id)
  }
  function clearSelectedNodes(): void {
    selectedNodes.value = []
  }
  function selectAllNodes(): void {
    selectedNodes.value = Object.keys(nodes.value)
  }

  function setXtermNode(node: ApiNode): void {
    currentXtremNode.value = node
  }
  function removeXtermNode(): void {
    currentXtremNode.value = null
  }

  return {
    nodes, selectedNodes, currentXtremNode,
    allNodes, getSelectedNodes,
    setNode, getNodeById, removeNode, clearNodes, setAllNodes,
    addToSelectedNodes, removeFromSelectedNodes, clearSelectedNodes, selectAllNodes,
    setXtermNode, removeXtermNode,
  }
})
