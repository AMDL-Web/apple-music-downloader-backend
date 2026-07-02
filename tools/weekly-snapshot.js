#!/usr/bin/env node
import { execSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

/**
 * 执行命令并返回结果，静默处理错误
 */
function callCommand(command) {
  try {
    return execSync(command, { encoding: 'utf-8', stdio: ['pipe', 'pipe', 'pipe'] }).trim();
  } catch {
    return '';
  }
}

/**
 * 获取当前年份的后两位
 */
function getYearSuffix() {
  return new Date().getFullYear().toString().slice(-2);
}

/**
 * 获取当前是一年中的第几周
 * ISO 周数：周一为一周的开始，第一周包含该年的第一个周四
 */
function getWeekNumber() {
  const date = new Date();
  const d = new Date(Date.UTC(date.getFullYear(), date.getMonth(), date.getDate()));
  const dayNum = d.getUTCDay() || 7;
  d.setUTCDate(d.getUTCDate() + 4 - dayNum);
  const yearStart = new Date(Date.UTC(d.getUTCFullYear(), 0, 1));
  return Math.ceil((((d - yearStart) / 86400000) + 1) / 7);
}

/**
 * 获取所有现有的周快照标签
 */
function getWeeklySnapshotTags() {
  const tags = callCommand('git tag -l').split('\n').filter(Boolean);
  const weeklyPattern = /^\d{2}w\d{1,2}[a-z]$/;
  return tags.filter(tag => weeklyPattern.test(tag)).sort();
}

/**
 * 获取指定周的所有快照标签
 */
function getSnapshotsForWeek(yearSuffix, weekNum) {
  const tags = getWeeklySnapshotTags();
  const weekPrefix = `${yearSuffix}w${weekNum}`;
  return tags.filter(tag => tag.startsWith(weekPrefix));
}

/**
 * 获取下一个版本字母
 */
function getNextLetter(existingTags, weekPrefix) {
  if (existingTags.length === 0) {
    return 'a';
  }

  const letters = existingTags.map(tag => {
    const match = tag.match(/^\d{2}w\d{1,2}([a-z])$/);
    return match ? match[1] : '';
  }).filter(Boolean).sort();

  const lastLetter = letters[letters.length - 1];
  if (!lastLetter) return 'a';

  const charCode = lastLetter.charCodeAt(0);
  if (charCode >= 122) {
    throw new Error('已达到本周最大快照数量 (z)');
  }

  return String.fromCharCode(charCode + 1);
}

/**
 * 生成下一个周快照版本号
 */
function generateNextSnapshotVersion() {
  const yearSuffix = getYearSuffix();
  const weekNum = getWeekNumber();
  const weekPrefix = `${yearSuffix}w${weekNum}`;

  const existingTags = getSnapshotsForWeek(yearSuffix, weekNum);
  const nextLetter = getNextLetter(existingTags, weekPrefix);

  return `${weekPrefix}${nextLetter}`;
}

/**
 * 获取当前周快照的基础标签
 * 逻辑：获取最新发布的任何类型的 tag（无论是 Pre Release、Release 还是每周快照）
 */
function getBaseTag() {
  const allTags = callCommand('git tag -l --sort=-creatordate').split('\n').filter(Boolean);
  
  if (allTags.length > 0) {
    return allTags[0];
  }
  
  return null;
}

/**
 * 主函数
 */
function main() {
  try {
    if (process.argv.includes('--get-base-tag')) {
      const baseTag = getBaseTag();
      console.log(baseTag || '');
      return;
    }

    const nextVersion = generateNextSnapshotVersion();
    const baseTag = getBaseTag();

    console.log(nextVersion);

    if (process.argv.includes('--verbose')) {
      const yearSuffix = getYearSuffix();
      const weekNum = getWeekNumber();
      console.error(`年份后两位: ${yearSuffix}`);
      console.error(`周数: ${weekNum}`);
      console.error(`基础标签: ${baseTag || '无'}`);
      console.error(`下一个快照版本: ${nextVersion}`);
    }
  } catch (error) {
    console.error('生成周快照版本号失败:', error.message);
    process.exit(1);
  }
}

export {
  getYearSuffix,
  getWeekNumber,
  getWeeklySnapshotTags,
  getSnapshotsForWeek,
  getNextLetter,
  generateNextSnapshotVersion,
  getBaseTag,
};

const isDirectRun = process.argv[1] === fileURLToPath(import.meta.url);
if (isDirectRun) {
  main();
}
