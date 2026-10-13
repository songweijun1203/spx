#ifndef SPX_PENDING_CONTROLS_H
#define SPX_PENDING_CONTROLS_H

#include "core/os/mutex.h"

// 跨线程提交 SPX 生命周期控制命令的进程级邮箱。
// 生产者无需访问 SpxEngine/SceneTree；同类请求会合并，优先级和实际执行顺序由
// Spx::on_update() 决定。该设计满足 Godot 规定：SceneTree/Node 变更只能在主线程执行。
// 直接调用方：Spx 的 reset/restart/pause/resume/next_frame；顶层调用方：Go runtime 控制 API。
class SpxPendingControls {
public:
	// 可合并的控制种类；COUNT 只作为固定数组长度，不是可提交命令。
	enum Kind { RESTART, RESET, PAUSE, RESUME, NEXT_FRAME, COUNT };

private:
	// 保护本类全部可变状态；生产者可来自 Go runtime 线程，消费者是 Godot 主线程。
	mutable Mutex mutex;
	// 是否接受新命令；SceneTree 未就绪或正在销毁时为 false。
	bool accepting = false;
	// 各类命令的待处理位；重复提交只保留一次，避免控制命令无界堆积。
	bool pending[COUNT] = {};
	// 最近一次 RESET 携带的退出码；仅在 pending[RESET] 为 true 时有意义。
	int reset_code = 0;
	// SPX 暂停状态的线程安全镜像，供非主线程 is_paused() 查询。
	bool paused = false;

public:
	// 开启/关闭邮箱并清空旧局全部状态。
	// 直接调用方：Spx::on_start()/on_destroy()；顶层调用方：Godot 主循环生命周期。
	void set_accepting(bool p_accepting) {
		MutexLock lock(mutex);
		accepting = p_accepting;
		for (bool &request : pending) {
			request = false;
		}
		reset_code = 0;
		paused = false;
	}

	// 合并提交一个控制命令；不会直接触碰 Godot 对象。
	// 直接调用方：非主线程中的 Spx 控制入口；顶层调用方：Go runtime 控制 API。
	void submit(Kind p_kind, int p_reset_code = 0) {
		MutexLock lock(mutex);
		if (!accepting) {
			return;
		}
		pending[p_kind] = true;
		if (p_kind == RESET) {
			reset_code = p_reset_code;
		}
	}

	// 原子取走指定命令；直接调用方：Spx::on_update()，顶层调用方：Godot 每帧主循环。
	bool take(Kind p_kind, int *r_reset_code = nullptr) {
		MutexLock lock(mutex);
		if (!pending[p_kind]) {
			return false;
		}
		pending[p_kind] = false;
		if (p_kind == RESET && r_reset_code) {
			*r_reset_code = reset_code;
		}
		return true;
	}

	// 更新暂停状态镜像；直接调用方：SpxEngine 的暂停/reset 内部流程。
	void set_paused(bool p_paused) {
		MutexLock lock(mutex);
		paused = accepting && p_paused;
	}

	// 线程安全查询暂停状态；直接调用方：Spx::is_paused()/SpxExtMgr，顶层调用方：Go API。
	bool is_paused() const {
		MutexLock lock(mutex);
		return paused;
	}
};

#endif // SPX_PENDING_CONTROLS_H
