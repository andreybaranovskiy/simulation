/**
 * Decoder for the binary playback chunk the server produces.
 *
 * A chunk is one window of simulated time, self-contained so that seeking to
 * any moment is a single fetch rather than a replay from the start. Its layout
 * is fixed by internal/runstore/chunk.go and mirrored here:
 *
 *   magic    8 bytes, "SIMCHUNK"
 *   version  uint32
 *   length   uint32, the JSON header's byte length
 *   header   JSON
 *   body     gzip, containing fixed-width records in five runs
 *
 * The header is deliberately outside the compressed body, so a client can see
 * what a chunk holds and decide whether to inflate it at all.
 */

export interface ChunkHeader {
  index: number
  startTime: number
  endTime: number
  liveCount: number
  spanCount: number
  stateCount: number
  spawnCount: number
  exitCount: number
}

/**
 * An entity already in the model when the window opens, together with the
 * motion it is part-way through.
 *
 * Carrying the whole span rather than just a position is what lets the viewer
 * place an entity correctly at any moment in the window, including while it is
 * crossing a distance that takes longer than the window itself.
 */
export interface Live {
  id: number
  cls: number
  state: number
  spanStart: number
  sx: number
  sy: number
  sz: number
  spanEnd: number
  ex: number
  ey: number
  ez: number
}

/** One leg of motion, ending inside the window. It begins wherever the
 *  previous one ended, which is why only the endpoint is stored. */
export interface Span {
  id: number
  endTime: number
  x: number
  y: number
  z: number
}

export interface StateChange {
  id: number
  time: number
  state: number
}

export interface Spawn {
  id: number
  cls: number
  time: number
  x: number
  y: number
  z: number
}

export interface Exit {
  id: number
  time: number
}

export interface Chunk {
  header: ChunkHeader
  live: Live[]
  spans: Span[]
  states: StateChange[]
  spawns: Spawn[]
  exits: Exit[]
}

const MAGIC = 'SIMCHUNK'
const FORMAT_VERSION = 1

// Record widths, fixed by the writer.
const LIVE_SIZE = 47
const SPAN_SIZE = 24
const STATE_SIZE = 13
const SPAWN_SIZE = 26
const EXIT_SIZE = 12

/** Decodes a chunk from the bytes the server returned. */
export async function decodeChunk(buffer: ArrayBuffer): Promise<Chunk> {
  const view = new DataView(buffer)

  if (buffer.byteLength < 16) {
    throw new Error('The chunk is too short to be valid.')
  }

  const magic = new TextDecoder().decode(new Uint8Array(buffer, 0, 8))
  if (magic !== MAGIC) {
    throw new Error('That file is not a playback chunk.')
  }

  const version = view.getUint32(8, true)
  if (version !== FORMAT_VERSION) {
    throw new Error(
      `This viewer reads chunk format ${FORMAT_VERSION}, but the run is format ${version}. Reload the page.`,
    )
  }

  const headerLength = view.getUint32(12, true)
  const headerJson = new TextDecoder().decode(new Uint8Array(buffer, 16, headerLength))
  const header = JSON.parse(headerJson) as ChunkHeader

  const compressed = buffer.slice(16 + headerLength)
  const body = await inflate(compressed)

  return readBody(header, body)
}

/**
 * Inflates the chunk body.
 *
 * DecompressionStream is used rather than a bundled inflate library: it is
 * native, so it neither costs bundle size nor blocks the main thread the way a
 * JavaScript implementation would on a chunk with thousands of entities.
 */
async function inflate(compressed: ArrayBuffer): Promise<DataView> {
  if (typeof DecompressionStream === 'undefined') {
    throw new Error('This browser cannot decompress playback data. Use a current version of Chrome, Edge, Firefox or Safari.')
  }

  const stream = new Blob([compressed]).stream().pipeThrough(new DecompressionStream('gzip'))
  const inflated = await new Response(stream).arrayBuffer()
  return new DataView(inflated)
}

function readBody(header: ChunkHeader, body: DataView): Chunk {
  const expected =
    header.liveCount * LIVE_SIZE +
    header.spanCount * SPAN_SIZE +
    header.stateCount * STATE_SIZE +
    header.spawnCount * SPAWN_SIZE +
    header.exitCount * EXIT_SIZE

  if (body.byteLength < expected) {
    throw new Error(
      `The chunk is truncated: ${body.byteLength} bytes where ${expected} were expected.`,
    )
  }

  let offset = 0

  const live: Live[] = new Array(header.liveCount)
  for (let i = 0; i < header.liveCount; i++) {
    live[i] = {
      id: body.getUint32(offset, true),
      cls: body.getUint16(offset + 4, true),
      state: body.getUint8(offset + 6),
      spanStart: body.getFloat64(offset + 7, true),
      sx: body.getFloat32(offset + 15, true),
      sy: body.getFloat32(offset + 19, true),
      sz: body.getFloat32(offset + 23, true),
      spanEnd: body.getFloat64(offset + 27, true),
      ex: body.getFloat32(offset + 35, true),
      ey: body.getFloat32(offset + 39, true),
      ez: body.getFloat32(offset + 43, true),
    }
    offset += LIVE_SIZE
  }

  const spans: Span[] = new Array(header.spanCount)
  for (let i = 0; i < header.spanCount; i++) {
    spans[i] = {
      id: body.getUint32(offset, true),
      endTime: body.getFloat64(offset + 4, true),
      x: body.getFloat32(offset + 12, true),
      y: body.getFloat32(offset + 16, true),
      z: body.getFloat32(offset + 20, true),
    }
    offset += SPAN_SIZE
  }

  const states: StateChange[] = new Array(header.stateCount)
  for (let i = 0; i < header.stateCount; i++) {
    states[i] = {
      id: body.getUint32(offset, true),
      time: body.getFloat64(offset + 4, true),
      state: body.getUint8(offset + 12),
    }
    offset += STATE_SIZE
  }

  const spawns: Spawn[] = new Array(header.spawnCount)
  for (let i = 0; i < header.spawnCount; i++) {
    spawns[i] = {
      id: body.getUint32(offset, true),
      cls: body.getUint16(offset + 4, true),
      time: body.getFloat64(offset + 6, true),
      x: body.getFloat32(offset + 14, true),
      y: body.getFloat32(offset + 18, true),
      z: body.getFloat32(offset + 22, true),
    }
    offset += SPAWN_SIZE
  }

  const exits: Exit[] = new Array(header.exitCount)
  for (let i = 0; i < header.exitCount; i++) {
    exits[i] = {
      id: body.getUint32(offset, true),
      time: body.getFloat64(offset + 4, true),
    }
    offset += EXIT_SIZE
  }

  return { header, live, spans, states, spawns, exits }
}
