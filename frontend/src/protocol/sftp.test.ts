import { describe, it, expect } from 'vitest'
import { parseWSMessage } from './ws'
import {
  sftpLogin,
  sftpList,
  sftpMkdir,
  sftpDelete,
  sftpRename,
  sftpDownload,
  sftpUploadInit,
  sftpUploadComplete,
  type SFTPFile,
} from './sftp'
import type { ApiNode } from './types'

const node: ApiNode = {
  name: 'n1',
  host: '1.2.3.4',
  port: 22,
  username: 'root',
  auth_type: 'password',
  auth_value: 'pwd',
}

describe('sftpLogin', () => {
  it('produces envelope with type=login and node as data', () => {
    const msg = parseWSMessage(sftpLogin(node))
    expect(msg!.type).toBe('login')
    expect(msg!.data).toEqual(node)
  })
})

describe('sftpList', () => {
  it('produces envelope with type=list and path data', () => {
    const msg = parseWSMessage(sftpList('/home'))
    expect(msg!.type).toBe('list')
    expect(msg!.data).toEqual({ path: '/home' })
  })
})

describe('sftpMkdir', () => {
  it('produces envelope with type=mkdir and path data', () => {
    const msg = parseWSMessage(sftpMkdir('/new'))
    expect(msg!.type).toBe('mkdir')
    expect(msg!.data).toEqual({ path: '/new' })
  })
})

describe('sftpDelete', () => {
  it('produces envelope with type=delete and path data', () => {
    const msg = parseWSMessage(sftpDelete('/file'))
    expect(msg!.type).toBe('delete')
    expect(msg!.data).toEqual({ path: '/file' })
  })
})

describe('sftpRename', () => {
  it('produces envelope with type=rename and old_path/new_path data', () => {
    const msg = parseWSMessage(sftpRename('/old', '/new'))
    expect(msg!.type).toBe('rename')
    expect(msg!.data).toEqual({ old_path: '/old', new_path: '/new' })
  })
})

describe('sftpDownload', () => {
  it('produces envelope with type=download, path and default offset=0', () => {
    const msg = parseWSMessage(sftpDownload('/file'))
    expect(msg!.type).toBe('download')
    expect(msg!.data).toEqual({ path: '/file', offset: 0 })
  })

  it('carries custom offset for resume', () => {
    const msg = parseWSMessage(sftpDownload('/file', 1024))
    expect(msg!.data).toEqual({ path: '/file', offset: 1024 })
  })
})

describe('sftpUploadInit', () => {
  it('produces envelope with type=upload_init and chunked upload fields', () => {
    const msg = parseWSMessage(sftpUploadInit('/r/f.txt', 'f.txt', 1024, 1 << 20))
    expect(msg!.type).toBe('upload_init')
    expect(msg!.data).toEqual({
      remote_path: '/r/f.txt',
      filename: 'f.txt',
      total_size: 1024,
      chunk_size: 1 << 20,
    })
  })
})

describe('sftpUploadComplete', () => {
  it('produces envelope with type=upload_complete and upload_id', () => {
    const msg = parseWSMessage(sftpUploadComplete('abc123'))
    expect(msg!.type).toBe('upload_complete')
    expect(msg!.data).toEqual({ upload_id: 'abc123' })
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
