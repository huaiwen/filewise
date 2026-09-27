//! A bounded child of the same native executable, not an interpreter or converter.
use crate::{Result, documents::Extraction, fail, files::MAX_FILE};
use std::{
    alloc::{GlobalAlloc, Layout, System},
    io::{Read, Write},
    path::PathBuf,
    process::{Command, Stdio},
    sync::{
        OnceLock,
        atomic::{AtomicUsize, Ordering},
    },
    time::{Duration, Instant},
};

const OUTPUT_LIMIT: usize = 24 * 1024 * 1024;
const HEAP_LIMIT: usize = 384 * 1024 * 1024;
static EXECUTABLE: OnceLock<PathBuf> = OnceLock::new();
static ALLOCATED: AtomicUsize = AtomicUsize::new(0);
static LIMIT: AtomicUsize = AtomicUsize::new(usize::MAX);

/// Used by the CLI. Embedders must point this at their installed Filewise binary.
pub fn executable(path: PathBuf) -> Result<()> {
    if !path.is_absolute() || !path.is_file() {
        return Err(fail(
            422,
            "Document worker requires an absolute executable path",
        ));
    }
    if let Err(path) = EXECUTABLE.set(path) {
        if EXECUTABLE.get() != Some(&path) {
            return Err(fail(409, "Document worker executable already configured"));
        }
    }
    Ok(())
}

/// Bound Rust heap allocations only in the parsing child. Keeping accounting from
/// process start makes freeing allocations made before the limit was set safe.
pub struct Allocator;
fn reserve(size: usize) -> bool {
    ALLOCATED
        .fetch_update(Ordering::Relaxed, Ordering::Relaxed, |used| {
            used.checked_add(size)
                .filter(|n| *n <= LIMIT.load(Ordering::Relaxed))
        })
        .is_ok()
}
// SAFETY: every allocation/deallocation uses System with the caller's original
// pointer and layout; failed reservations return null without touching the pointer.
unsafe impl GlobalAlloc for Allocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        if !reserve(layout.size()) {
            return std::ptr::null_mut();
        }
        let pointer = unsafe { System.alloc(layout) };
        if pointer.is_null() {
            ALLOCATED.fetch_sub(layout.size(), Ordering::Relaxed);
        }
        pointer
    }
    unsafe fn dealloc(&self, pointer: *mut u8, layout: Layout) {
        unsafe { System.dealloc(pointer, layout) };
        ALLOCATED.fetch_sub(layout.size(), Ordering::Relaxed);
    }
    unsafe fn realloc(&self, pointer: *mut u8, layout: Layout, size: usize) -> *mut u8 {
        // Conservatively reserve the new block before releasing the old one.
        if !reserve(size) {
            return std::ptr::null_mut();
        }
        let new = unsafe { System.realloc(pointer, layout, size) };
        ALLOCATED.fetch_sub(
            if new.is_null() { size } else { layout.size() },
            Ordering::Relaxed,
        );
        new
    }
}

pub fn run(format: &str) -> Result<Extraction> {
    use rustix::process::{Resource, Rlimit, getrlimit, setrlimit};
    LIMIT.store(HEAP_LIMIT, Ordering::Relaxed);
    for (resource, value) in [(Resource::Cpu, 15), (Resource::Core, 0)] {
        let limit = getrlimit(resource)
            .maximum
            .map_or(value, |hard| hard.min(value));
        setrlimit(
            resource,
            Rlimit {
                current: Some(limit),
                maximum: Some(limit),
            },
        )?;
    }
    let mut bytes = Vec::new();
    std::io::stdin()
        .take((MAX_FILE + 1) as u64)
        .read_to_end(&mut bytes)?;
    if bytes.len() > MAX_FILE {
        return Err(fail(413, "Document input exceeds 10 MiB"));
    }
    Ok(crate::documents::parse(format, &bytes)
        .unwrap_or_else(|e| Extraction::failed(format, &e.message)))
}

pub fn extract(format: &str, bytes: &[u8]) -> Result<Extraction> {
    let exe = EXECUTABLE
        .get()
        .ok_or_else(|| fail(503, "Document worker is not configured"))?;
    let mut child = Command::new(exe)
        .args(["extract-worker", format])
        .env_clear()
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()?;
    let (Some(input), Some(output)) = (child.stdin.take(), child.stdout.take()) else {
        let _ = child.kill();
        let _ = child.wait();
        return Err(fail(500, "Missing worker pipes"));
    };
    // Drain both pipes while enforcing the deadline; never wait for a full pipe.
    let (status, written, received) = std::thread::scope(|scope| {
        let writer = scope.spawn(move || {
            let mut input = input;
            input.write_all(bytes)
        });
        let reader = scope.spawn(move || {
            let mut result = vec![];
            output
                .take((OUTPUT_LIMIT + 1) as u64)
                .read_to_end(&mut result)
                .map(|_| result)
        });
        let deadline = Instant::now() + Duration::from_secs(20);
        let status = loop {
            match child.try_wait() {
                Ok(Some(status)) => break Ok(status),
                Err(e) => {
                    let _ = child.kill();
                    let _ = child.wait();
                    break Err(e.into());
                }
                Ok(None) if Instant::now() >= deadline => {
                    let _ = child.kill();
                    let _ = child.wait();
                    break Err(fail(413, "Document worker exceeded its deadline"));
                }
                Ok(None) => std::thread::sleep(Duration::from_millis(10)),
            }
        };
        (status, writer.join(), reader.join())
    });
    if !status?.success() {
        return Err(fail(
            422,
            "Document worker failed or exceeded resource limits",
        ));
    }
    written.map_err(|_| fail(500, "Document writer failed"))??;
    let result = received.map_err(|_| fail(500, "Document reader failed"))??;
    if result.len() > OUTPUT_LIMIT {
        return Err(fail(413, "Document worker output exceeds limit"));
    }
    let result: Extraction = serde_json::from_slice(&result)?;
    result.validate()?;
    Ok(result)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn allocator_refuses_growth_without_losing_the_original_block() {
        LIMIT.store(96, Ordering::Relaxed);
        // SAFETY: layouts and live pointers match every System allocation; a failed
        // realloc leaves the original pointer valid and it is freed exactly once.
        unsafe {
            let layout = Layout::from_size_align(64, 8).unwrap();
            let pointer = Allocator.alloc(layout);
            assert!(!pointer.is_null());
            pointer.write_bytes(42, 64);
            assert!(Allocator.realloc(pointer, layout, 128).is_null());
            assert_eq!(*pointer.add(63), 42);
            assert_eq!(ALLOCATED.load(Ordering::Relaxed), 64);
            let smaller = Allocator.realloc(pointer, layout, 16);
            assert!(!smaller.is_null());
            assert_eq!(*smaller.add(15), 42);
            assert_eq!(ALLOCATED.load(Ordering::Relaxed), 16);
            Allocator.dealloc(smaller, Layout::from_size_align(16, 8).unwrap());
        }
        assert_eq!(ALLOCATED.load(Ordering::Relaxed), 0);
        LIMIT.store(usize::MAX, Ordering::Relaxed);
    }
}
