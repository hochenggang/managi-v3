import { describe, it, expect } from 'vitest'
import type { FileOperation, SFTPFile, SFTPRequest, SFTPResponse } from '@/protocol/sftp'

describe('protocol/sftp', () => {
  describe('FileOperation literals', () => {
    // 与后端 model.FileOperationType 常量对齐（表驱动）
    const expected: FileOperation[] = [
      'list', 'mkdir', 'delete', 'rename', 'move',
      'upload', 'download',
      'upload_init', 'upload_chunk', 'upload_complete',
    ]

    it.each(expected)('operation "%s" is a valid literal', (op) => {
      expect(typeof op).toBe('string')
    })

    it('contains all 10 operations including v3 chunked upload', () => {
      expect(expected).toHaveLength(10)
      expect(expected).toContain('upload_init')
      expect(expected).toContain('upload_chunk')
      expect(expected).toContain('upload_complete')
    })
  })

  describe('SFTPFile shape', () => {
    it('has expected field names', () => {
      const file: SFTPFile = {
        filename: 'a.txt',
        size: 100,
        mode: '0644',
        is_dir: false,
        mtime: 1700000000,
      }
      expect(Object.keys(file).sort()).toEqual(
        ['filename', 'size', 'mode', 'is_dir', 'mtime'].sort(),
      )
    })

    it('mode is string (v3 change from v2 number)', () => {
      const file: SFTPFile = {
        filename: 'd', size: 0, mode: '0755', is_dir: true, mtime: 0,
      }
      expect(typeof file.mode).toBe('string')
    })
  })

  describe('SFTPRequest shape', () => {
    it('minimal list request has operation + remote_path', () => {
      const req: SFTPRequest = { operation: 'list', remote_path: '/home' }
      expect(req.operation).toBe('list')
      expect(req.remote_path).toBe('/home')
    })

    it('upload_init request carries chunked upload fields', () => {
      const req: SFTPRequest = {
        operation: 'upload_init',
        remote_path: '/r/f.txt',
        filename: 'f.txt',
        total_size: 1024,
        chunk_size: 1 << 20,
      }
      expect(req.filename).toBe('f.txt')
      expect(req.total_size).toBe(1024)
      expect(req.chunk_size).toBe(1 << 20)
    })

    it('upload_complete request carries upload_id', () => {
      const req: SFTPRequest = {
        operation: 'upload_complete',
        remote_path: '/r/f.txt',
        upload_id: 'abc123',
      }
      expect(req.upload_id).toBe('abc123')
    })
  })

  describe('SFTPResponse shape', () => {
    it('list response carries files array', () => {
      const resp: SFTPResponse = {
        success: true,
        files: [{ filename: 'x', size: 1, mode: '0644', is_dir: false, mtime: 0 }],
      }
      expect(resp.files).toHaveLength(1)
      expect(resp.success).toBe(true)
    })

    it('upload_init response carries upload_id and uploaded_offset', () => {
      const resp: SFTPResponse = {
        success: true,
        upload_id: 'u1',
        uploaded_offset: 0,
      }
      expect(resp.upload_id).toBe('u1')
      expect(resp.uploaded_offset).toBe(0)
    })

    it('download_start response carries total', () => {
      const resp: SFTPResponse = {
        success: true,
        type: 'download_start',
        total: 1048576,
      }
      expect(resp.type).toBe('download_start')
      expect(resp.total).toBe(1048576)
    })

    it('complete response carries filename and complete flag', () => {
      const resp: SFTPResponse = {
        success: true,
        complete: true,
        filename: 'downloaded.txt',
      }
      expect(resp.complete).toBe(true)
      expect(resp.filename).toBe('downloaded.txt')
    })
  })
})
