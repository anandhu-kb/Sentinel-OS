export namespace guardian {
	
	export class BackupInfo {
	    id: number;
	    projectPath: string;
	    archivePath: string;
	    tier: string;
	    sizeBytes: number;
	    createdAt: string;
	
	    static createFrom(source: any = {}) {
	        return new BackupInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.projectPath = source["projectPath"];
	        this.archivePath = source["archivePath"];
	        this.tier = source["tier"];
	        this.sizeBytes = source["sizeBytes"];
	        this.createdAt = source["createdAt"];
	    }
	}
	export class Schedule {
	    id: number;
	    projectPath: string;
	    intervalHours: number;
	    lastRun: string;
	
	    static createFrom(source: any = {}) {
	        return new Schedule(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.projectPath = source["projectPath"];
	        this.intervalHours = source["intervalHours"];
	        this.lastRun = source["lastRun"];
	    }
	}

}

export namespace main {
	
	export class PomodoroSession {
	    id: number;
	    startTime: string;
	    endTime?: string;
	    durationMinutes: number;
	    status: string;
	    taskName: string;
	
	    static createFrom(source: any = {}) {
	        return new PomodoroSession(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.startTime = source["startTime"];
	        this.endTime = source["endTime"];
	        this.durationMinutes = source["durationMinutes"];
	        this.status = source["status"];
	        this.taskName = source["taskName"];
	    }
	}
	export class ResourceStats {
	    cpuPercent: number;
	    ramPercent: number;
	    diskPercent: number;
	
	    static createFrom(source: any = {}) {
	        return new ResourceStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cpuPercent = source["cpuPercent"];
	        this.ramPercent = source["ramPercent"];
	        this.diskPercent = source["diskPercent"];
	    }
	}
	export class UptimeLogRow {
	    id: number;
	    target: string;
	    status: string;
	    latencyMs: number;
	    checkedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new UptimeLogRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.target = source["target"];
	        this.status = source["status"];
	        this.latencyMs = source["latencyMs"];
	        this.checkedAt = source["checkedAt"];
	    }
	}

}

export namespace sentinel {
	
	export class StatusSnapshot {
	    id: number;
	    name: string;
	    address: string;
	    kind: string;
	    isUp: boolean;
	    latencyMs: number;
	    checkedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new StatusSnapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.address = source["address"];
	        this.kind = source["kind"];
	        this.isUp = source["isUp"];
	        this.latencyMs = source["latencyMs"];
	        this.checkedAt = source["checkedAt"];
	    }
	}

}

export namespace snapshot {
	
	export class DiffSummary {
	    filesAdded: number;
	    filesDeleted: number;
	    filesModified: number;
	    linesAdded: number;
	    linesDeleted: number;
	
	    static createFrom(source: any = {}) {
	        return new DiffSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filesAdded = source["filesAdded"];
	        this.filesDeleted = source["filesDeleted"];
	        this.filesModified = source["filesModified"];
	        this.linesAdded = source["linesAdded"];
	        this.linesDeleted = source["linesDeleted"];
	    }
	}
	export class FileDiff {
	    relPath: string;
	    linesAdded: number;
	    linesDeleted: number;
	
	    static createFrom(source: any = {}) {
	        return new FileDiff(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.relPath = source["relPath"];
	        this.linesAdded = source["linesAdded"];
	        this.linesDeleted = source["linesDeleted"];
	    }
	}
	export class DiffResult {
	    oldSnapshotId: number;
	    newSnapshotId: number;
	    addedFiles: string[];
	    deletedFiles: string[];
	    modifiedFiles: FileDiff[];
	    summary: DiffSummary;
	
	    static createFrom(source: any = {}) {
	        return new DiffResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.oldSnapshotId = source["oldSnapshotId"];
	        this.newSnapshotId = source["newSnapshotId"];
	        this.addedFiles = source["addedFiles"];
	        this.deletedFiles = source["deletedFiles"];
	        this.modifiedFiles = this.convertValues(source["modifiedFiles"], FileDiff);
	        this.summary = this.convertValues(source["summary"], DiffSummary);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	export class SnapshotInfo {
	    id: number;
	    projectPath: string;
	    commitHash: string;
	    fullHash: string;
	    message: string;
	    fileCount: number;
	    totalBytes: number;
	    createdAt: string;
	
	    static createFrom(source: any = {}) {
	        return new SnapshotInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.projectPath = source["projectPath"];
	        this.commitHash = source["commitHash"];
	        this.fullHash = source["fullHash"];
	        this.message = source["message"];
	        this.fileCount = source["fileCount"];
	        this.totalBytes = source["totalBytes"];
	        this.createdAt = source["createdAt"];
	    }
	}

}

export namespace watcher {
	
	export class AppHistoryEntry {
	    date: string;
	    totalSecs: number;
	
	    static createFrom(source: any = {}) {
	        return new AppHistoryEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.totalSecs = source["totalSecs"];
	    }
	}
	export class AppStat {
	    exeName: string;
	    totalSecs: number;
	
	    static createFrom(source: any = {}) {
	        return new AppStat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.exeName = source["exeName"];
	        this.totalSecs = source["totalSecs"];
	    }
	}
	export class CategoryStat {
	    category: string;
	    totalSecs: number;
	    apps: AppStat[];
	
	    static createFrom(source: any = {}) {
	        return new CategoryStat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.category = source["category"];
	        this.totalSecs = source["totalSecs"];
	        this.apps = this.convertValues(source["apps"], AppStat);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DetailedLog {
	    appName: string;
	    category: string;
	    date: string;
	    startedAt: string;
	    endedAt: string;
	    durationSecs: number;
	
	    static createFrom(source: any = {}) {
	        return new DetailedLog(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.appName = source["appName"];
	        this.category = source["category"];
	        this.date = source["date"];
	        this.startedAt = source["startedAt"];
	        this.endedAt = source["endedAt"];
	        this.durationSecs = source["durationSecs"];
	    }
	}
	export class HistoryEntry {
	    date: string;
	    stats: CategoryStat[];
	
	    static createFrom(source: any = {}) {
	        return new HistoryEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.stats = this.convertValues(source["stats"], CategoryStat);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Rule {
	    ID: number;
	    MatchString: string;
	    Name: string;
	    Category: string;
	    IsRegex: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Rule(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ID = source["ID"];
	        this.MatchString = source["MatchString"];
	        this.Name = source["Name"];
	        this.Category = source["Category"];
	        this.IsRegex = source["IsRegex"];
	    }
	}
	export class WindowInfo {
	    title: string;
	    exeName: string;
	    category: string;
	    startedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new WindowInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.title = source["title"];
	        this.exeName = source["exeName"];
	        this.category = source["category"];
	        this.startedAt = source["startedAt"];
	    }
	}

}

