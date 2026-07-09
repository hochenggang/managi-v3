import { describe, it, expect, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useNodesStore } from '@/stores/nodesStore'
import type { ApiNode } from '@/protocol/types'

const testNode: ApiNode = {
  name: 'node1',
  host: '1.2.3.4',
  port: 22,
  username: 'root',
  auth_type: 'password',
  auth_value: 'pass',
}

const testNode2: ApiNode = {
  ...testNode,
  name: 'node2',
  host: '5.6.7.8',
}

describe('useNodesStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  describe('node CRUD', () => {
    it('setNode + getNodeById roundtrip', () => {
      const store = useNodesStore()
      store.setNode(testNode)
      expect(store.getNodeById('1.2.3.4:22')).toEqual(testNode)
    })

    it('getNodeById returns undefined for unknown id', () => {
      const store = useNodesStore()
      expect(store.getNodeById('unknown')).toBeUndefined()
    })

    it('removeNode removes from nodes and selection', () => {
      const store = useNodesStore()
      store.setNode(testNode)
      store.addToSelectedNodes('1.2.3.4:22')
      store.removeNode('1.2.3.4:22')
      expect(store.getNodeById('1.2.3.4:22')).toBeUndefined()
      expect(store.selectedNodes).not.toContain('1.2.3.4:22')
    })

    it('clearNodes clears nodes and selection', () => {
      const store = useNodesStore()
      store.setNode(testNode)
      store.addToSelectedNodes('1.2.3.4:22')
      store.clearNodes()
      expect(store.allNodes).toHaveLength(0)
      expect(store.selectedNodes).toHaveLength(0)
    })

    it('setAllNodes batch sets multiple nodes', () => {
      const store = useNodesStore()
      store.setAllNodes([testNode, testNode2])
      expect(store.allNodes).toHaveLength(2)
    })
  })

  describe('selection', () => {
    it('addToSelectedNodes deduplicates', () => {
      const store = useNodesStore()
      store.setNode(testNode)
      store.addToSelectedNodes('1.2.3.4:22')
      store.addToSelectedNodes('1.2.3.4:22')
      expect(store.selectedNodes).toHaveLength(1)
    })

    it('removeFromSelectedNodes removes specific id', () => {
      const store = useNodesStore()
      store.setNode(testNode)
      store.setNode(testNode2)
      store.addToSelectedNodes('1.2.3.4:22')
      store.addToSelectedNodes('5.6.7.8:22')
      store.removeFromSelectedNodes('1.2.3.4:22')
      expect(store.selectedNodes).toEqual(['5.6.7.8:22'])
    })

    it('clearSelectedNodes empties selection', () => {
      const store = useNodesStore()
      store.setNode(testNode)
      store.addToSelectedNodes('1.2.3.4:22')
      store.clearSelectedNodes()
      expect(store.selectedNodes).toHaveLength(0)
    })

    it('selectAllNodes selects all node ids', () => {
      const store = useNodesStore()
      store.setNode(testNode)
      store.setNode(testNode2)
      store.selectAllNodes()
      expect(store.selectedNodes).toHaveLength(2)
      expect(store.selectedNodes).toContain('1.2.3.4:22')
      expect(store.selectedNodes).toContain('5.6.7.8:22')
    })

    it('getSelectedNodes returns node objects', () => {
      const store = useNodesStore()
      store.setNode(testNode)
      store.addToSelectedNodes('1.2.3.4:22')
      expect(store.getSelectedNodes).toEqual([testNode])
    })

    it('allNodes getter returns array', () => {
      const store = useNodesStore()
      store.setNode(testNode)
      store.setNode(testNode2)
      expect(store.allNodes).toHaveLength(2)
    })
  })

  describe('xterm node', () => {
    it('setXtremNode + removeXtremNode', () => {
      const store = useNodesStore()
      expect(store.currentXtremNode).toBeNull()
      store.setXtremNode(testNode)
      expect(store.currentXtremNode).toEqual(testNode)
      store.removeXtremNode()
      expect(store.currentXtremNode).toBeNull()
    })
  })
})
